// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package signer

import (
	"bytes"
	"encoding/binary"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/WICG/webpackage/go/signedexchange"
	"github.com/ampproject/amppackager/packager/rtv"
	pkgt "github.com/ampproject/amppackager/packager/testing"
	"github.com/ampproject/amppackager/packager/util"
	rpb "github.com/ampproject/amppackager/transformer/request"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/suite"
)

var fakePath = "/amp/secret-life-of-pine-trees.html"
var fakeBody = []byte("<html amp><body>They like to OPINE. Get it? (Is he fir real? Yew gotta be kidding me.)")
var transformedBody = []byte("<html amp><head></head><body>They like to OPINE. Get it? (Is he fir real? Yew gotta be kidding me.)</body></html>")

func headerNames(headers http.Header) []string {
	names := make([]string, len(headers))
	i := 0
	for name := range headers {
		names[i] = strings.ToLower(name)
		i++
	}
	sort.Strings(names)
	return names
}

type SignerSuite struct {
	suite.Suite
	httpServer, tlsServer *httptest.Server
	httpsClient           *http.Client
	shouldPackage         bool
	fakeHandler           func(resp http.ResponseWriter, req *http.Request)
	lastRequestURL        string
}

func (this *SignerSuite) new(urlSets []util.URLSet) *Signer {
	handler, err := New(pkgt.Certs[0], pkgt.Key, urlSets, &rtv.RTVCache{}, func() bool { return this.shouldPackage }, nil, true)
	this.Require().NoError(err)
	// Accept the self-signed certificate generated by the test server.
	handler.client = this.httpsClient
	return handler
}

func (this *SignerSuite) get(t *testing.T, handler pkgt.AlmostHandler, target string) *http.Response {
	return this.getP(t, handler, target, httprouter.Params{})
}

func (this *SignerSuite) getP(t *testing.T, handler pkgt.AlmostHandler, target string, params httprouter.Params) *http.Response {
	return pkgt.GetHP(t, handler, target, http.Header{
		"AMP-Cache-Transform": {"google"}, "Accept": {"application/signed-exchange;v=b2"}}, params)
}

func (this *SignerSuite) getB(t *testing.T, handler pkgt.AlmostHandler, target string, body string) *http.Response {
	return pkgt.GetBH(t, handler, target, strings.NewReader(body), http.Header{
		"AMP-Cache-Transform": {"google"}, "Accept": {"application/signed-exchange;v=b2"}})
}

func (this *SignerSuite) httpURL() string {
	return this.httpServer.URL
}

func (this *SignerSuite) httpHost() string {
	u, err := url.Parse(this.httpURL())
	this.Require().NoError(err)
	return u.Host
}

// Same port as httpURL, but with an HTTPS scheme.
func (this *SignerSuite) httpSignURL() string {
	u, err := url.Parse(this.httpURL())
	this.Require().NoError(err)
	u.Scheme = "https"
	return u.String()
}

func (this *SignerSuite) httpsURL() string {
	return this.tlsServer.URL
}

func (this *SignerSuite) httpsHost() string {
	u, err := url.Parse(this.httpsURL())
	this.Require().NoError(err)
	return u.Host
}

func (this *SignerSuite) SetupSuite() {
	// Mock out example.com endpoint.
	this.httpServer = httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		this.fakeHandler(resp, req)
	}))

	this.tlsServer = httptest.NewTLSServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		this.fakeHandler(resp, req)
	}))
	this.httpsClient = this.tlsServer.Client()
	// Configure the test httpsClient to have the same redirect policy as production.
	this.httpsClient.CheckRedirect = noRedirects
}

func (this *SignerSuite) TearDownSuite() {
	this.httpServer.Close()
	this.tlsServer.Close()
}

func (this *SignerSuite) SetupTest() {
	this.shouldPackage = true
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		this.lastRequestURL = req.URL.String()
		resp.Header().Set("Content-Type", "text/html")
		resp.Write(fakeBody)
	}
	// Don't actually do any transforms. Only parse & print.
	getTransformerRequest = func(r *rtv.RTVCache, s, u string) *rpb.Request {
		return &rpb.Request{Html: string(s), DocumentUrl: u, Config: rpb.Request_NONE,
			AllowedFormats: []rpb.Request_HtmlFormat{rpb.Request_AMP}}
	}
}

func (this *SignerSuite) TestSimple() {
	urlSets := []util.URLSet{{
		Sign:  &util.URLPattern{[]string{"https"}, "", this.httpHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
		Fetch: &util.URLPattern{[]string{"http"}, "", this.httpHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, boolPtr(true)},
	}}
	resp := this.get(this.T(), this.new(urlSets),
		"/priv/doc?fetch="+url.QueryEscape(this.httpURL()+fakePath)+
			"&sign="+url.QueryEscape(this.httpSignURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)
	this.Assert().Equal("google", resp.Header.Get("AMP-Cache-Transform"))
	this.Assert().Equal("nosniff", resp.Header.Get("X-Content-Type-Options"))
	this.Assert().Equal(fakePath, this.lastRequestURL)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(this.httpSignURL()+fakePath, exchange.RequestURI.String())
	this.Assert().Equal(http.Header{":method": []string{"GET"}}, exchange.RequestHeaders)
	this.Assert().Equal(200, exchange.ResponseStatus)
	this.Assert().Equal(
		[]string{"content-encoding", "content-length", "content-security-policy", "content-type", "date", "digest", "x-content-type-options"},
		headerNames(exchange.ResponseHeaders))
	this.Assert().Equal("text/html", exchange.ResponseHeaders.Get("Content-Type"))
	this.Assert().Equal("nosniff", exchange.ResponseHeaders.Get("X-Content-Type-Options"))
	this.Assert().Contains(exchange.SignatureHeaderValue, "validity-url=\""+this.httpSignURL()+"/amppkg/validity\"")
	this.Assert().Contains(exchange.SignatureHeaderValue, "integrity=\"digest/mi-sha256-03\"")
	this.Assert().Contains(exchange.SignatureHeaderValue, "cert-url=\""+this.httpSignURL()+"/amppkg/cert/k9GCZZIDzAt2X0b2czRv0c2omW5vgYNh6ZaIz_UNTRQ\"")
	this.Assert().Contains(exchange.SignatureHeaderValue, "cert-sha256=*k9GCZZIDzAt2X0b2czRv0c2omW5vgYNh6ZaIz/UNTRQ=*")
	// TODO(twifkak): Control date, and test for expires and sig.
	// The response header values are untested here, as that is covered by signedexchange tests.

	// For small enough bodies, the only thing that MICE does is add a record size prefix.
	var payloadPrefix bytes.Buffer
	binary.Write(&payloadPrefix, binary.BigEndian, uint64(miRecordSize))
	this.Assert().Equal(append(payloadPrefix.Bytes(), transformedBody...), exchange.Payload)
}

func (this *SignerSuite) TestParamsInPostBody() {
	urlSets := []util.URLSet{{
		Sign:  &util.URLPattern{[]string{"https"}, "", this.httpHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
		Fetch: &util.URLPattern{[]string{"http"}, "", this.httpHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, boolPtr(true)},
	}}
	resp := this.getB(this.T(), this.new(urlSets), "/priv/doc",
		"fetch="+url.QueryEscape(this.httpURL()+fakePath)+
			"&sign="+url.QueryEscape(this.httpSignURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)
	this.Assert().Equal(fakePath, this.lastRequestURL)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(this.httpSignURL()+fakePath, exchange.RequestURI.String())
}

func (this *SignerSuite) TestNoFetchParam() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakePath, this.lastRequestURL)
	this.Assert().Equal(this.httpsURL()+fakePath, exchange.RequestURI.String())
}

func (this *SignerSuite) TestSignAsPathParam() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	resp := this.getP(this.T(), this.new(urlSets), `/priv/doc/`, httprouter.Params{httprouter.Param{"signURL", "/" + this.httpsURL() + fakePath}})
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakePath, this.lastRequestURL)
	this.Assert().Equal(this.httpsURL()+fakePath, exchange.RequestURI.String())
}

func (this *SignerSuite) TestPreservesContentType() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html;charset=utf-8;v=5")
		resp.Write(fakeBody)
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal("text/html;charset=utf-8;v=5", exchange.ResponseHeaders.Get("Content-Type"))
}

func (this *SignerSuite) TestRemovesLinkHeaders() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Link", "rel=preload;<http://1.2.3.4/>")
		resp.Write(fakeBody)
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().NotContains(exchange.ResponseHeaders, http.CanonicalHeaderKey("Link"))
}

func (this *SignerSuite) TestRemovesStatefulHeaders() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Set-Cookie", "yum yum yum")
		resp.Write(fakeBody)
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().NotContains(exchange.ResponseHeaders, http.CanonicalHeaderKey("Set-Cookie"))
}

func (this *SignerSuite) TestAddsLinkHeaders() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Write([]byte("<html amp><head><link rel=stylesheet href=foo><script src=bar>"))
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal("<foo>;rel=preload;as=style,<bar>;rel=preload;as=script", exchange.ResponseHeaders.Get("Link"))
}

func (this *SignerSuite) TestEscapesLinkHeaders() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		// This shouldn't happen for valid AMP, and AMP Caches should
		// verify the Link header so that it wouldn't be ingested.
		// However, it would be nice to limit the impact that could be
		// caused by transformation of an invalid AMP, e.g. on a
		// same-origin impression.
		resp.Write([]byte(`<html amp><head><script src="https://foo.com/a,b>c">`))
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	exchange, err := signedexchange.ReadExchange(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal("<https://foo.com/a,b%3Ec>;rel=preload;as=script", exchange.ResponseHeaders.Get("Link"))
}

func (this *SignerSuite) TestErrorNoCache() {
	urlSets := []util.URLSet{{
		Fetch: &util.URLPattern{[]string{"http"}, "", this.httpHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, boolPtr(true)},
	}}
	// Missing sign param generates an error.
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?fetch="+url.QueryEscape(this.httpURL()+fakePath))
	this.Assert().Equal(http.StatusBadRequest, resp.StatusCode, "incorrect status: %#v", resp)
	this.Assert().Equal("no-store", resp.Header.Get("Cache-Control"))
}

func (this *SignerSuite) TestProxyUnsignedIfRedirect() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Set-Cookie", "yum yum yum")
		resp.Header().Set("Location", "/login")
		resp.WriteHeader(301)
	}

	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(301, resp.StatusCode)
	this.Assert().Equal("yum yum yum", resp.Header.Get("set-cookie"))
	this.Assert().Equal("/login", resp.Header.Get("location"))
}

func (this *SignerSuite) TestProxyUnsignedIfNotModified() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Cache-control", "private")
		resp.Header().Set("Cookie", "yum yum yum")
		resp.Header().Set("ETag", "superrad")
		resp.WriteHeader(304)
	}

	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(304, resp.StatusCode)
	this.Assert().Equal("private", resp.Header.Get("cache-control"))
	this.Assert().Equal("", resp.Header.Get("cookie"))
	this.Assert().Equal("superrad", resp.Header.Get("etag"))
}

func (this *SignerSuite) TestProxyUnsignedIfShouldntPackage() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	this.shouldPackage = false
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)
	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakeBody, body, "incorrect body: %#v", resp)
}

func (this *SignerSuite) TestProxyUnsignedIfMissingAMPCacheTransformHeader() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	resp := pkgt.GetH(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath), http.Header{
		"Accept": {"application/signed-exchange;v=b2"}})
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)
	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakeBody, body, "incorrect body: %#v", resp)
}

func (this *SignerSuite) TestProxyUnsignedIfMissingAcceptHeader() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	resp := pkgt.GetH(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath), http.Header{
		"AMP-Cache-Transform": {"google"}})
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)
	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakeBody, body, "incorrect body: %#v", resp)
}

func (this *SignerSuite) TestProxyUnsignedErrOnStatefulHeader() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), true, nil},
	}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Set-Cookie", "chocolate chip")
		resp.Header().Set("Content-Type", "text/html")
		resp.WriteHeader(200)
	}

	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(200, resp.StatusCode)
	this.Assert().Equal("chocolate chip", resp.Header.Get("Set-Cookie"))
	this.Assert().Equal("text/html", resp.Header.Get("Content-Type"))
}

func (this *SignerSuite) TestProxyUnsignedNonCachable() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Cache-Control", "no-store")
		resp.Header().Set("Content-Type", "text/html")
		resp.WriteHeader(200)
	}

	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(200, resp.StatusCode)
	this.Assert().Equal("no-store", resp.Header.Get("Cache-Control"))
	this.Assert().Equal("text/html", resp.Header.Get("Content-Type"))
}

func (this *SignerSuite) TestProxyUnsignedIfNotAMP() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	nonAMPBody := []byte("<html><body>They like to OPINE. Get it? (Is he fir real? Yew gotta be kidding me.)")
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html")
		resp.Write(nonAMPBody)
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(nonAMPBody, body, "incorrect body: %#v", resp)
}

func (this *SignerSuite) TestProxyUnsignedIfWrongAMP() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil}}}
	wrongAMPBody := []byte("<html amp4email><body>They like to OPINE. Get it? (Is he fir real? Yew gotta be kidding me.)")
	this.fakeHandler = func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("Content-Type", "text/html")
		resp.Write(wrongAMPBody)
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(http.StatusOK, resp.StatusCode, "incorrect status: %#v", resp)

	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(wrongAMPBody, body, "incorrect body: %#v", resp)
}

func (this *SignerSuite) TestProxyTransformError() {
	urlSets := []util.URLSet{{
		Sign: &util.URLPattern{[]string{"https"}, "", this.httpsHost(), stringPtr("/amp/.*"), []string{}, stringPtr(""), false, nil},
	}}

	// Generate a request for non-existent transformer that will fail
	getTransformerRequest = func(r *rtv.RTVCache, s, u string) *rpb.Request {
		return &rpb.Request{Html: string(s), DocumentUrl: u, Config: rpb.Request_CUSTOM,
			AllowedFormats: []rpb.Request_HtmlFormat{rpb.Request_AMP},
			Transformers:   []string{"bogus"}}
	}
	resp := this.get(this.T(), this.new(urlSets), "/priv/doc?sign="+url.QueryEscape(this.httpsURL()+fakePath))
	this.Assert().Equal(200, resp.StatusCode)
	this.Assert().Equal("text/html", resp.Header.Get("Content-Type"))

	body, err := ioutil.ReadAll(resp.Body)
	this.Require().NoError(err)
	this.Assert().Equal(fakeBody, body, "incorrect body: %#v", resp)
}

func TestSignerSuite(t *testing.T) {
	suite.Run(t, new(SignerSuite))
}
