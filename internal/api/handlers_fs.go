package api

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// fsDialString is the domain-level dial-string mod_xml_curl returns so
// FreeSWITCH can construct a dial URI for `user/<id>@<domain>`. The
// sofia_contact() lookup pulls the registered contact (incl. fs_path for
// WebSocket NAT traversal). Without it, FS rejects originate with
// MANDATORY_IE_MISSING even when the user is registered. Shared by the
// agent and guest directory responses.
const fsDialString = "{^^:sip_invite_domain=${dialed_domain}:presence_id=${dialed_user}@${dialed_domain}}${sofia_contact(*/${dialed_user}@${dialed_domain})}"

// FSBasicAuth gates the /fs/* endpoints with HTTP Basic Auth. The single
// shared credential is configured per Tring deployment via
// TRING_FS_BASIC_AUTH=user:pass and is also configured on the FreeSWITCH side
// in autoload_configs/xml_curl.conf.xml's `gateway-credentials` parameter.
// Defense-in-depth assumes FreeSWITCH and the API live on the same trust
// boundary; this auth prevents accidental external reach.
func (s *Server) FSBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != s.fsBasicUser || p != s.fsBasicPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="tring-fs"`)
			writeError(w, http.StatusUnauthorized, "bad fs credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// XML shapes for FreeSWITCH mod_xml_curl directory responses.
type fsDocument struct {
	XMLName xml.Name   `xml:"document"`
	Type    string     `xml:"type,attr"`
	Section *fsSection `xml:"section"`
}

type fsSection struct {
	Name   string    `xml:"name,attr"`
	Domain *fsDomain `xml:"domain,omitempty"`
	Result *fsResult `xml:"result,omitempty"`
}

type fsDomain struct {
	Name   string   `xml:"name,attr"`
	Params fsParams `xml:"params"`
	User   fsUser   `xml:"user"`
}

type fsUser struct {
	ID        string      `xml:"id,attr"`
	Params    fsParams    `xml:"params"`
	Variables fsVariables `xml:"variables"`
}

type fsParams struct {
	Params []fsKV `xml:"param"`
}
type fsVariables struct {
	Variables []fsKV `xml:"variable"`
}
type fsKV struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}
type fsResult struct {
	Status string `xml:"status,attr"`
}

// handleFSDirectory implements FreeSWITCH's mod_xml_curl directory binding.
// FreeSWITCH POSTs form-encoded params describing the lookup it needs
// (section=directory, user=alice, domain=t_dev.tring.local, ...). We return
// an XML document — either the user's directory entry (with their stored SIP
// password) or a result section saying "not found".
func (s *Server) handleFSDirectory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeFSNotFound(w)
		return
	}
	if r.PostForm.Get("section") != "directory" {
		writeFSNotFound(w)
		return
	}

	user := firstNonEmpty(r.PostForm.Get("user"), r.PostForm.Get("key_value"))
	domain := firstNonEmpty(r.PostForm.Get("domain"), r.PostForm.Get("sip_auth_realm"))
	if user == "" || domain == "" {
		writeFSNotFound(w)
		return
	}

	var tenantID uuid.UUID
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetTenantIDBySipDomain, domain).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeFSNotFound(w)
		return
	}
	if err != nil {
		writeFSNotFound(w)
		return
	}

	// Guest web-callers register with an ephemeral `guest_*` SIP identity
	// minted by POST /v1/sdk/guest-credentials. They resolve against
	// guest_sessions (not agents) and are tagged user_class=guest + pinned
	// to a flow, which the dialplan (tring_guest_ivr) and IVR executor use
	// to keep them inside the IVR — a guest credential never bridges a trunk.
	if strings.HasPrefix(user, "guest_") {
		s.writeGuestDirectory(w, r, tenantID, domain, user)
		return
	}

	var sipPassword string
	var agentID uuid.UUID
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentCredentials,
		tenantID, user,
	).Scan(&agentID, &sipPassword)
	if err != nil {
		writeFSNotFound(w)
		return
	}

	doc := fsDocument{
		Type: "freeswitch/xml",
		Section: &fsSection{
			Name: "directory",
			Domain: &fsDomain{
				Name: domain,
				Params: fsParams{Params: []fsKV{
					{Name: "dial-string", Value: fsDialString}, // see const fsDialString
				}},
				User: fsUser{
					ID: user,
					Params: fsParams{Params: []fsKV{
						{Name: "password", Value: sipPassword},
					}},
					Variables: fsVariables{Variables: []fsKV{
						{Name: "user_context", Value: "default"},
						{Name: "tenant_id", Value: tenantID.String()},
						{Name: "agent_id", Value: agentID.String()},
					}},
				},
			},
		},
	}
	writeFSXML(w, doc)
}

// writeGuestDirectory resolves an ephemeral guest SIP identity against
// guest_sessions and returns its directory entry tagged user_class=guest
// and tring_flow_id. FreeSWITCH applies these as channel variables when it
// authenticates the guest's INVITE; the dialplan keys on user_class to
// route the call into the IVR only, and the executor reads tring_flow_id to
// pick the flow without a dialed DID. Expired or flow-less sessions 404.
func (s *Server) writeGuestDirectory(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, domain, user string) {
	var (
		sipPassword string
		flowID      *uuid.UUID
		clientRef   string
	)
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetGuestSession,
		tenantID, user).Scan(&sipPassword, &flowID, &clientRef)
	if err != nil || flowID == nil {
		writeFSNotFound(w)
		return
	}
	vars := []fsKV{
		{Name: "user_context", Value: "default"},
		{Name: "user_class", Value: "guest"},
		{Name: "tenant_id", Value: tenantID.String()},
		{Name: "tring_flow_id", Value: flowID.String()},
		// Carry the session username so the executor can expire it when the
		// call ends — single-use credentials.
		{Name: "tring_guest_session", Value: user},
	}
	// When the embedder identified the caller (e.g. a logged-in support
	// portal), carry it onto the channel so the executor records the client
	// on the call row instead of the anonymous guest_* identity.
	if clientRef != "" {
		vars = append(vars, fsKV{Name: "tring_client_ref", Value: clientRef})
	}
	writeFSXML(w, fsDocument{
		Type: "freeswitch/xml",
		Section: &fsSection{
			Name: "directory",
			Domain: &fsDomain{
				Name: domain,
				Params: fsParams{Params: []fsKV{
					{Name: "dial-string", Value: fsDialString},
				}},
				User: fsUser{
					ID: user,
					Params: fsParams{Params: []fsKV{
						{Name: "password", Value: sipPassword},
					}},
					Variables: fsVariables{Variables: vars},
				},
			},
		},
	})
}

func writeFSNotFound(w http.ResponseWriter) {
	writeFSXML(w, fsDocument{
		Type: "freeswitch/xml",
		Section: &fsSection{
			Name:   "result",
			Result: &fsResult{Status: "not found"},
		},
	})
}

func writeFSXML(w http.ResponseWriter, doc fsDocument) {
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(doc)
}

func firstNonEmpty(args ...string) string {
	for _, a := range args {
		if a != "" {
			return a
		}
	}
	return ""
}
