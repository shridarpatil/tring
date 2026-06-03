package api

import (
	"encoding/xml"
	"errors"
	"net/http"

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
