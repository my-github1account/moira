package remote

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func (remote *Remote) prepareRequest(from, until int64, target string) (*http.Request, error) {
	req, err := http.NewRequest("GET", remote.config.URL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("format", "json")
	q.Add("from", strconv.FormatInt(from, 10))
	q.Add("target", target)
	q.Add("until", strconv.FormatInt(until, 10))
	req.URL.RawQuery = q.Encode()
	if remote.config.User != "" && remote.config.Password != "" {
		req.SetBasicAuth(remote.config.User, remote.config.Password)
	}
	return req, nil
}

func (remote *Remote) makeRequest(req *http.Request) ([]byte, error) {
	var body []byte

	resp, err := remote.client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return body, fmt.Errorf("The remote server is not available or the response was reset by timeout. "+ //nolint
			"TTL: %s, PATH: %s, ERROR: %v ", remote.client.Timeout.String(), req.URL.RawPath, err)
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return body, err
	}

	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("bad response status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
