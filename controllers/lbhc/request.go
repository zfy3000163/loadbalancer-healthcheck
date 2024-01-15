package lbhc

import (
	"crypto/tls"
	"io/ioutil"
	"net/http"

	//"bytes"
	"encoding/base64"
	"io"
	"log"
)

// JSONContentType - type used in communication with manager
const JSONContentType = "application/json"

// DataContentType - binary only data, like archives
const DataContentType = "application/octet-stream"

// HTTPClient - Credentials for cloudify
type HTTPClient struct {
	restURL  string
	user     string
	password string
	tenant   string
	debug    bool
	//cacert   string
}

func (r *HTTPClient) debugLogf(format string, v ...interface{}) {
	if r.debug {
		log.Printf(format, v...)
	}
}

func httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// getRequest - create new request by params
func (r *HTTPClient) getRequest(url, method string, body io.Reader) (*http.Request, error) {
	r.debugLogf("Use: %v %v:%v@%v#%s\n", method, r.user, r.password, r.restURL+url, r.tenant)

	//var authString string
	authString := r.user + ":" + r.password
	req, err := http.NewRequest(method, r.restURL+url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(authString)))
	if len(r.tenant) > 0 {
		req.Header.Add("Tenant", r.tenant)
	}

	return req, nil
}

func (r *HTTPClient) Get(url, acceptedContentType string) ([]byte, error) {
	req, err := r.getRequest(url, "GET", nil)
	if err != nil {
		return []byte{}, err
	}

	client := httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return []byte{}, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, err
	}

	if acceptedContentType == JSONContentType {
		r.debugLogf("Response %s\n", string(body))
	} else {
		r.debugLogf("Binary response length: %d\n", len(body))
	}

	return body, nil
}

/*func main() {

	req := &HTTPClient{
		restURL: "http://10.121.120.76:8000/",
		debug:   true,
	}
	body, _ := req.Get("status=down", JSONContentType)
	fmt.Print(body)
	req.debugLogf("Response %s\n", string(body))

}
*/
