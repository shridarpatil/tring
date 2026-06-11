package ivr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// httpRespBodyCap bounds how much of a callback response we read.
const httpRespBodyCap = 1 << 20 // 1 MiB

// RunHTTPCallback performs an http_callback node's request — templating {{vars}}
// into the URL + body — and returns the branch outcome plus, on a 2xx with a
// JSON object body, the top-level fields stringified (for the caller to stash
// into flow vars / CDR channel vars). It never errors the call: a bad URL or a
// failed/non-2xx request returns "http:non2xx". This is the transport-agnostic
// core shared by the FreeSWITCH executor and the pion IVR driver.
func RunHTTPCallback(ctx context.Context, n *Node, vars map[string]string, client *http.Client) (outcome string, stash map[string]string, err error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := SubstituteVars(StringFromConfig(n.Config, "url"), vars)
	if url == "" {
		return "http:non2xx", nil, nil
	}
	method := strings.ToUpper(StringFromConfig(n.Config, "method"))
	if method == "" {
		method = "GET"
	}

	var body io.Reader
	bodyTmpl := StringFromConfig(n.Config, "body_template")
	if bodyTmpl != "" {
		body = strings.NewReader(SubstituteVars(bodyTmpl, vars))
	}
	req, reqErr := http.NewRequestWithContext(ctx, method, url, body)
	if reqErr != nil {
		return "http:non2xx", nil, nil
	}
	if bodyTmpl != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range MapStringFromConfig(n.Config, "headers") {
		req.Header.Set(k, v)
	}

	resp, doErr := client.Do(req)
	if doErr != nil {
		return "http:non2xx", nil, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, httpRespBodyCap))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "http:non2xx", nil, nil
	}

	var obj map[string]any
	if json.Unmarshal(respBody, &obj) == nil {
		stash = make(map[string]string, len(obj))
		for k, v := range obj {
			stash[k] = StringifyJSON(v)
		}
	}
	return "http:2xx", stash, nil
}
