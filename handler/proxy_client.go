package handler

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/signer/v4"
	log "github.com/sirupsen/logrus"
)

// Client is an interface to make testing http.Client calls easier
type Client interface {
	Do(req *http.Request) (*http.Response, error)
}

// ProxyClient implements the Client interface
type ProxyClient struct {
	Signer *v4.Signer
	Client Client
	Region string
}

func (p *ProxyClient) sign(req *http.Request, service *endpoints.ResolvedEndpoint) error {
	body := bytes.NewReader([]byte{})

	if req.Body != nil {
		b, err := ioutil.ReadAll(req.Body)
		if err != nil {
			return err
		}

		body = bytes.NewReader(b)
	}

	var err error
	switch service.SigningMethod {
	case "v4", "s3v4":
		_, err = p.Signer.Sign(req, body, service.SigningName, service.SigningRegion, time.Now())
		break
	case "s3":
		_, err = p.Signer.Presign(req, body, service.SigningName, service.SigningRegion, time.Duration(time.Hour), time.Now())
		break
	default:
		err = fmt.Errorf("unable to sign with specified signing method %s for service %s", service.SigningMethod, service.SigningName)
		break
	}

	if err == nil {
		log.WithFields(log.Fields{"service": service.SigningName, "region": service.SigningRegion}).Debug("signed request")
	}

	return err
}

func (p *ProxyClient) Do(req *http.Request) (*http.Response, error) {
	overwriteHost := os.Getenv("SERVICE_HOST")
	if overwriteHost != "" {
		req.Host = overwriteHost
	}
	proxyURL := *req.URL
	proxyURL.Host = req.Host
	proxyURL.Scheme = "https"

	proxyReq, err := http.NewRequest(req.Method, proxyURL.String(), req.Body)
	if err != nil {
		return nil, err
	}

	service := determineAWSServiceFromHost(req.Host)
	if service == nil {
		return nil, fmt.Errorf("unable to determine service from host: %s", req.Host)
	}

	if err := p.sign(proxyReq, service); err != nil {
		return nil, err
	}

	// add headers after request is signed
	for k, vv := range req.Header {
		if _, ok := proxyReq.Header[k]; !ok {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}
	}

	if log.GetLevel() == log.DebugLevel {
		proxyReqDump, err := httputil.DumpRequest(proxyReq, true)
		if err != nil {
			log.WithError(err).Error("unable to dump request")
		}
		log.WithField("request", string(proxyReqDump)).Debug("proxying request")
	}

	resp, err := p.Client.Do(proxyReq)
	if err != nil {
		return nil, err
	}

	if log.GetLevel() == log.DebugLevel && resp.StatusCode >= 400 {
		b, _ := ioutil.ReadAll(resp.Body)
		log.WithField("message", string(b)).Error("error proxying request")
	}

	return resp, nil
}
