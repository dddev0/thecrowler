// Copyright 2023 Paolo Fabio Zaino
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package httpinfo provides functionality to extract HTTP header information
package httpinfo

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	cmn "github.com/pzaino/thecrowler/pkg/common"
	cfg "github.com/pzaino/thecrowler/pkg/config"
	ruleset "github.com/pzaino/thecrowler/pkg/ruleset"
	"golang.org/x/net/publicsuffix"
)

const (
	errMatchingSignature = "matching signature: %v"
)

// CreateConfig creates a default Config
func CreateConfig(url string, c cfg.Config) Config {
	sel := c.Selenium[0]
	usrAgent := cmn.UsrAgentStrMap[sel.Type+"-desktop01"]
	return Config{
		URL:             url,
		CustomHeader:    map[string]string{"User-Agent": usrAgent},
		FollowRedirects: true,
		Timeout:         60,
		SSLMode:         "none",
	}
}

// Check if the URL is valid and allowed
func validateURL(inputURL string) (bool, error) {
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return false, err
	}

	// Ensure the scheme is http or https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return false, fmt.Errorf("invalid URL scheme: %s", parsedURL.Scheme)
	}

	// Add more checks as needed, e.g., against a domain whitelist
	return true, nil
}

// ExtractHTTPInfo extracts HTTP header information based on the provided configuration
func ExtractHTTPInfo(config Config, re *ruleset.RuleEngine, htmlContent string) (*HTTPDetails, error) {
	cmn.DebugMsg(cmn.DbgLvlDebug3, "Extracting HTTP information for URL: %s", config.URL)

	// Validate the URL
	if ok, err := validateURL(config.URL); !ok {
		return nil, err
	}

	// Validate IP address
	if err := validateIPAddress(config.URL); err != nil {
		return nil, err
	}

	// Retrieve SSL Info (if it's HTTPS)
	cmn.DebugMsg(cmn.DbgLvlDebug1, "Collecting SSL/TLS information for URL: %s", config.URL)
	sslInfo, err := getSSLInfo(&config)
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlError, "retrieving SSL information: %v", err)
	}

	// Create a new HTTP client
	httpClient := createHTTPClient(config)

	// Send HTTP request
	cmn.DebugMsg(cmn.DbgLvlDebug1, "Collecting HTTP Header information for URL: %s", config.URL)
	resp, err := sendHTTPRequest(httpClient, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Handle redirects
	if shouldFollowRedirects(config, resp) {
		return handleRedirects(config, re, resp)
	}

	// Create a new HTTPDetails object
	info := new(HTTPDetails)

	// Collect response headers
	info.ResponseHeaders = resp.Header

	// Extract response headers
	info.URL = config.URL
	info.CustomHeaders = config.CustomHeader
	info.FollowRedirects = config.FollowRedirects
	info.SSLInfo, err = ConvertSSLInfoToDetails(*sslInfo)
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlDebug1, "Error converting SSL info to details: %v", err)
	}

	// Analyze response body for additional information
	detectedItems, err := analyzeResponse(resp, info, sslInfo, re, &htmlContent)
	if err != nil {
		return nil, err
	}
	info.DetectedEntities = make(map[string]DetectedEntity)
	for k, v := range detectedItems {
		info.DetectedEntities[k] = v
	}

	return info, nil
}

func validateIPAddress(url string) error {
	host := urlToHost(url)
	ips := cmn.HostToIP(host)
	for _, ip := range ips {
		if cmn.IsDisallowedIP(ip, 0) {
			return fmt.Errorf("IP address not allowed: %s", url)
		}
	}
	return nil
}

func getSSLInfo(config *Config) (*SSLInfo, error) {
	// Check if the URL has a port number, if so, extract the port number
	url := strings.TrimSpace(config.URL)
	port := ""
	// first let's remove the scheme
	if strings.HasPrefix(strings.ToLower(url), "http") {
		url = strings.Replace(url, "http://", "", 1)
		url = strings.Replace(url, "https://", "", 1)
		port = "443"
	} else if strings.HasPrefix(strings.ToLower(url), "ftp") {
		url = strings.Replace(url, "ftp://", "", 1)
		url = strings.Replace(url, "ftps://", "", 1)
		port = "21"
	} else if strings.HasPrefix(strings.ToLower(url), "ws:") ||
		strings.HasPrefix(strings.ToLower(url), "wss:") {
		url = strings.Replace(url, "ws://", "", 1)
		url = strings.Replace(url, "wss://", "", 1)
		port = "80"
	}
	// now let's check if there is a port number
	if strings.Contains(url, ":") {
		// extract the port number
		port = strings.Split(url, ":")[1]
		// remove the port number from the URL
		url = strings.Split(url, ":")[0]
	}

	cmn.DebugMsg(cmn.DbgLvlDebug1, "URL: %s, Port: %s", url, port)

	// Get the SSL information
	sslInfo := NewSSLInfo()
	//err := sslInfo.GetSSLInfo(url, port)
	err := sslInfo.CollectSSLData(url, port, config)
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlDebug1, "Error retrieving SSL information: %v", err)
	}

	// Validate the SSL certificate
	err = sslInfo.ValidateCertificate()
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlDebug1, "Error validating SSL certificate: %v", err)
	}

	return sslInfo, nil
}

func createHTTPClient(config Config) *http.Client {
	transport := cmn.SafeTransport(config.Timeout, "ignore")
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, // Skip TLS certificate verification
		MinVersion:         tls.VersionTLS10,
		MaxVersion:         tls.VersionTLS13,
	}
	sn := urlToDomain(config.URL)
	transport.TLSClientConfig.ServerName = sn

	if len(config.Proxies) > 0 {
		proxyURL, err := url.Parse(config.Proxies[0].Address)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
		if config.Proxies[0].Username != "" {
			transport.ProxyConnectHeader = http.Header{}
			transport.ProxyConnectHeader.Set("Proxy-Authorization", basicAuth(config.Proxies[0].Username, config.Proxies[0].Password))
		}
	}

	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return handleRedirect(req, via, config, transport)
		},
	}
	return httpClient
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return "Basic " + cmn.Base64Encode(auth)
}

func sendHTTPRequest(httpClient *http.Client, config Config) (*http.Response, error) {
	req, err := http.NewRequest("GET", config.URL, nil)
	if err != nil {
		return nil, err
	}

	// Add custom headers if specified
	for key, value := range config.CustomHeader {
		req.Header.Add(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func shouldFollowRedirects(config Config, resp *http.Response) bool {
	return config.FollowRedirects && (resp.StatusCode >= 300 && resp.StatusCode < 400)
}

func handleRedirects(config Config, re *ruleset.RuleEngine, resp *http.Response) (*HTTPDetails, error) {
	newLocation := resp.Header.Get("Location")
	cmn.DebugMsg(cmn.DbgLvlDebug2, "Redirect location: %s", newLocation)

	newConfig := config
	newConfig.URL = newLocation
	newConfig.CustomHeader = map[string]string{"User-Agent": cmn.UsrAgentStrMap["desktop01"]}
	newConfig.FollowRedirects = true

	return ExtractHTTPInfo(newConfig, re, "")
}

// handleRedirect is a custom redirect handler that updates the ServerName for SNI in case of domain change due to redirect
func handleRedirect(req *http.Request, _ []*http.Request, config Config, transport *http.Transport) error {
	// TODO: rename _ to via and use it to check for infinite redirects
	if !config.FollowRedirects {
		return http.ErrUseLastResponse
	}

	// Update ServerName for SNI in case of domain change due to redirect
	lastURL, err := url.Parse(req.URL.String())
	if err != nil {
		return fmt.Errorf("error parsing redirect URL: %v", err)
	}
	lastDomain := lastURL.Hostname()
	req.URL.Scheme = "https"
	transport.TLSClientConfig.ServerName = lastDomain

	return nil
}

// AnalyzeResponse analyzes the response body and header for additional server-related information
// and possible technologies used
// Note: In the future this needs to be moved in http_rules logic
func analyzeResponse(resp *http.Response, info *HTTPDetails, sslInfo *SSLInfo, re *ruleset.RuleEngine, htmlContent *string) (map[string]DetectedEntity, error) {
	// Get the response headers
	header := &(*info).ResponseHeaders

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Convert the response body to a string
	responseBody := string(bodyBytes)
	if strings.TrimSpace(responseBody) == "" {
		// If the response body is empty, use the provided HTML content
		// (it is possible that a WAF or similar is blocking the request)
		responseBody = (*htmlContent)
	}

	// Initialize the infoList map
	// infoList := make(map[string]string)
	infoList := make(map[string]DetectedEntity)

	// Detect Entities on the page/site
	x := detectTechnologies(info.URL, responseBody, header, sslInfo, re)
	for k, v := range *x {
		infoList[k] = v
	}

	return infoList, nil
}

// detectionEntityDetails is used internally to represent the details of an entity detection
type detectionEntityDetails struct {
	entityType      string
	matchedPatterns []string
	confidence      float32
}

func detectTechnologies(url string, responseBody string,
	header *http.Header, sslInfo *SSLInfo,
	re *ruleset.RuleEngine) *map[string]DetectedEntity {
	// micro-signatures
	Patterns := re.GetAllEnabledDetectionRules()

	// Initialize a slice to store the detected stuff
	detectedTech := make(map[string]detectionEntityDetails)

	// Normalize the response body
	responseBody = strings.ToLower(strings.TrimSpace(responseBody))

	// Iterate through all the header tags and check for CMS signatures
	if header != nil {
		const (
			hostHeader = "Host-Header"
			xGenerator = "X-Generator"
		)
		for headerTag := range *header {
			// Get the HTTP header fields for the specific tag
			var Signatures map[string]map[string]ruleset.HTTPHeaderField
			if headerTag == hostHeader {
				Signatures = ruleset.GetAllHTTPHeaderFieldsMap(&Patterns)
			} else {
				Signatures = ruleset.GetHTTPHeaderFieldsMapByKey(&Patterns, headerTag)
			}
			if (Signatures != nil) && len(Signatures) > 0 {
				detectTechByTag(header, headerTag, &Signatures, &detectedTech)
			}
		}
		// Some extra tags that may help:
		if header.Get(xGenerator) != "" {
			entity := detectionEntityDetails{
				entityType:      "header_field",
				confidence:      10,
				matchedPatterns: []string{xGenerator},
			}
			detectedTech[xGenerator] = entity
		}
	}

	// Try to detect technologies using URL's micro-signatures (e.g., /wp-content/)
	URLSignatures := ruleset.GetAllURLMicroSignaturesMap(&Patterns)
	detectTechByURL(url, &URLSignatures, &detectedTech)
	URLSignatures = nil

	// Try to detect technologies using meta tags
	MetaTagsSignatures := ruleset.GetAllMetaTagsMap(&Patterns)
	detectTechByMetaTags(responseBody, &MetaTagsSignatures, &detectedTech)
	MetaTagsSignatures = nil

	// Check the response body for Technologies signatures
	Signatures := ruleset.GetAllPageContentPatternsMap(&Patterns)
	detectTechnologiesByKeyword(responseBody, &Signatures, &detectedTech)
	Signatures = nil

	// Check for SSL/TLS technologies
	if sslInfo != nil {
		sslSignatures := ruleset.GetAllSSLSignaturesMap(&Patterns)
		detectTechBySSL(sslInfo, &sslSignatures, &detectedTech)
		sslSignatures = nil
	}

	// Process implied technologies
	processImpliedTechnologies(&detectedTech, &Patterns)

	// Transform the detectedTech map into a map of strings
	detectedTechStr := make(map[string]DetectedEntity)
	for k, v := range detectedTech {
		// calculate "confidence" based on the value of x
		x := v.confidence
		c := calculateConfidence(x, re.DetectionConfig.NoiseThreshold, re.DetectionConfig.MaybeThreshold, re.DetectionConfig.DetectedThreshold)
		if c <= 10 {
			continue
		}
		v.confidence = c
		if x < 0 {
			// If x is negative, then the analysis was on the ABSENCE of a technology
			// In this case we add a special prefix to the entity name
			k = "no_" + k
		}
		entity := DetectedEntity{
			EntityName:      k,
			EntityType:      v.entityType,
			Confidence:      v.confidence,
			MatchedPatterns: v.matchedPatterns,
		}
		detectedTechStr[k] = entity
	}
	return &detectedTechStr
}

func processImpliedTechnologies(detectedTech *map[string]detectionEntityDetails, patterns *[]ruleset.DetectionRule) {
	for tech, details := range *detectedTech {
		for _, rule := range *patterns {
			if rule.ObjectName == tech {
				for _, impliedTech := range rule.GetImplies() {
					if _, alreadyDetected := (*detectedTech)[impliedTech]; !alreadyDetected {
						(*detectedTech)[impliedTech] = detectionEntityDetails{
							entityType:      "implied",
							confidence:      details.confidence,
							matchedPatterns: []string{"implied by " + tech},
						}
					}
				}
			}
		}
	}
}

func calculateConfidence(x, Noise, Maybe, Detected float32) float32 {
	// Confidence calculation based on the value of x
	if x < 0 {
		// If x is negative, then the analysis was on the ABSENCE of a technology
		// In this case, we want to calculate the confidence based on the absence of the technology
		// i.e. the further x is to 0, the higher the confidence
		return 100 - min(100, -x)
	} else if x < Noise {
		return (x / Noise) * 10 // Maps [0, Noise) to [0%, 10%]
	} else if x < Maybe {
		return 10 + ((x-Noise)/(Maybe-Noise))*30 // Maps [Noise, Maybe) to [10%, 40%]
	} else if x < Detected {
		return 40 + ((x-Maybe)/(Detected-Maybe))*60 // Maps [Maybe, Detected) to [40%, 100%]
	} else {
		// Maps [Detected, ∞) to [40%, 100%]
		// i.e. this ensures that confidence doesn't exceed 100%
		return min(100, 40+((x-Detected)/(Detected-Maybe))*60)
	}
}

func detectTechBySSL(sslInfo *SSLInfo, sslSignatures *map[string][]ruleset.SSLSignature, detectedTech *map[string]detectionEntityDetails) {
	for ObjName := range *sslSignatures {
		for _, signature := range (*sslSignatures)[ObjName] {
			detectSSLTechBySignatureValue(sslInfo.CertChain, signature, detectedTech, ObjName)
		}
	}
}

func detectSSLTechBySignatureValue(certChain []*x509.Certificate, signature ruleset.SSLSignature, detectedTech *map[string]detectionEntityDetails, ObjName string) {
	const (
		detectionType = "ssl_certificate"
	)
	for _, cert := range certChain {
		// Get Certificate field based on the signature key
		certField, err := getCertificateField(cert, signature.Key)
		if err != nil {
			continue
		} else {
			for _, signatureValue := range signature.Value {
				matched, err := regexp.MatchString(signatureValue, certField)
				if err != nil {
					cmn.DebugMsg(cmn.DbgLvlError, errMatchingSignature, err)
				} else if matched {
					//if strings.Contains(certField, signatureValue) {
					updateDetectedTech(detectedTech, ObjName, signature.Confidence, signatureValue)
					updateDetectedType(detectedTech, ObjName, detectionType)
				}
			}
		}
	}
}

func getCertificateField(cert *x509.Certificate, key string) (string, error) {
	sValue := reflect.ValueOf(cert.Subject)
	sType := sValue.Type()
	for i := 0; i < sValue.NumField(); i++ {
		if sType.Field(i).Name == key {
			return sValue.Field(i).String(), nil
		}
	}
	return "", fmt.Errorf("field not found: %s", key)
}

func detectTechnologiesByKeyword(responseBody string, signatures *map[string][]ruleset.PageContentSignature, detectedTech *map[string]detectionEntityDetails) {
	// Create a new document from the HTML string
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(responseBody))
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlError, "loading HTML: %s", err)
		return
	}
	// Iterate through all the signatures and check for possible technologies
	for sig := range *signatures {
		item := (*signatures)[sig]
		for _, signature := range item {
			detectTechBySignature(responseBody, doc, signature, sig, detectedTech)
		}
	}
}

func detectTechBySignature(responseBody string, doc *goquery.Document, signature ruleset.PageContentSignature, sig string, detectedTech *map[string]detectionEntityDetails) {
	if signature.Key == "*" {
		detectTechBySignatureValue(responseBody, signature.Signature, sig, detectedTech, signature.Confidence)
	} else {
		// prepare the signature key
		key := strings.ToLower(strings.TrimSpace(signature.Key))
		doc.Find(key).Each(func(index int, htmlItem *goquery.Selection) {
			var text1 string
			var text2 string
			var attrExists bool
			if (signature.Attribute != "") && (signature.Attribute != "text") {
				text1, attrExists = htmlItem.Attr(strings.ToLower(strings.TrimSpace(signature.Attribute)))
			}
			text2 = htmlItem.Text()
			if attrExists {
				detectTechBySignatureValue(text1, signature.Signature, sig, detectedTech, signature.Confidence)
			}
			if len(signature.Text) > 0 {
				detectTechBySignatureValue(text2, signature.Text, sig, detectedTech, signature.Confidence)
			}
		})
	}
}

func detectTechBySignatureValue(text string, signatures []string, sig string, detectedTech *map[string]detectionEntityDetails, confidence float32) {
	for _, sigValue := range signatures {
		if sigValue != "" {
			detectTechBySignatureValueHelper(text, sigValue, sig, detectedTech, confidence)
		}
	}
}

func detectTechBySignatureValueHelper(text string, sigValue string, sig string, detectedTech *map[string]detectionEntityDetails, confidence float32) {
	const detectionType = "html"
	if sigValue != "*" {
		matched, err := regexp.MatchString(sigValue, text)
		if err != nil {
			cmn.DebugMsg(cmn.DbgLvlError, errMatchingSignature, err)
		} else if matched {
			updateDetectedTech(detectedTech, sig, confidence, sigValue)
		}
	} else {
		// Just call updateDetectedTech if the signature is "*"
		updateDetectedTech(detectedTech, sig, confidence, "*")
	}
	updateDetectedType(detectedTech, sig, detectionType)
}

func updateDetectedTech(detectedTech *map[string]detectionEntityDetails, sig string, confidence float32, matchedSig string) {
	entity, ok := (*detectedTech)[sig]
	if ok {
		// If the entry exists, update its confidence and matched patterns
		entity.confidence += confidence
	} else {
		// Initialize a new entity if the entry doesn't exist
		entity.confidence = confidence
		entity.matchedPatterns = make([]string, 0)
	}
	// Append the pattern if it's not already added
	if !cmn.SliceContains(entity.matchedPatterns, matchedSig) {
		entity.matchedPatterns = append(entity.matchedPatterns, matchedSig)
	}

	// Save the updated entity back to the map
	(*detectedTech)[sig] = entity
}

func updateDetectedType(detectedTech *map[string]detectionEntityDetails, sig string, detectionType string) {
	entity := (*detectedTech)[sig]
	if entity.confidence != 0 {
		if entity.entityType == "" {
			entity.entityType = detectionType
		} else {
			if !strings.Contains(entity.entityType, detectionType) {
				entity.entityType += "," + detectionType
			}
		}
		(*detectedTech)[sig] = entity
	}
}

func detectTechByTag(header *http.Header, tagName string, detectRules *map[string]map[string]ruleset.HTTPHeaderField, detectedTech *map[string]detectionEntityDetails) {
	hh := (*header)[tagName] // get the header value (header tag name is case sensitive)
	tagName = strings.ToLower(tagName)
	if len(hh) != 0 {
		for _, tag := range hh {
			tag = strings.ToLower(tag)
			detectTechByTagHelper(tagName, tag, detectRules, detectedTech)
		}
	}
}

func detectTechByTagHelper(tagName string, tag string, detectRules *map[string]map[string]ruleset.HTTPHeaderField, detectedTech *map[string]detectionEntityDetails) {
	const (
		detectionType = "http_header"
	)
	for ObjName := range *detectRules {
		item := (*detectRules)[ObjName]
		for _, signature := range item[tagName].Value {
			if signature == "" {
				continue
			}
			if signature == "!*" {
				// "!*" This means check if the Signature Key is not present in the header.
				// Usually used for negative detection of headers like Content-Security-Policy
				// to identify if a site is secure or not.
				if !strings.Contains(tag, item[tagName].Key) {
					updateDetectedTech(detectedTech, ObjName, -item[tagName].Confidence, item[tagName].Key)
					continue
				}
			} else if signature != "*" {
				matched, err := regexp.MatchString(signature, tag)
				if err != nil {
					cmn.DebugMsg(cmn.DbgLvlError, errMatchingSignature, err)
					continue
				}
				if matched {
					updateDetectedTech(detectedTech, ObjName, item[tagName].Confidence, signature)
				}
			} else {
				updateDetectedTech(detectedTech, ObjName, item[tagName].Confidence, "*")
			}
			updateDetectedType(detectedTech, ObjName, detectionType)
		}
	}
}

func detectTechByMetaTags(responseBody string, signatures *map[string][]ruleset.MetaTag, detectedTech *map[string]detectionEntityDetails) {
	// Create a new document from the HTML string
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(responseBody))
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlError, "loading HTML: %s", err)
		return
	}
	const detectionType = "meta_tags"
	// Iterate through all the meta tags and check for possible technologies
	for ObjName := range *signatures {
		for _, signature := range (*signatures)[ObjName] {
			doc.Find("meta").Each(func(index int, htmlItem *goquery.Selection) {
				if strings.EqualFold(htmlItem.AttrOr("name", ""), strings.TrimSpace(signature.Name)) {
					text, contExists := htmlItem.Attr("content")
					if contExists && signature.Content != "" {
						text = strings.ToLower(text)
						matched, err := regexp.MatchString(signature.Content, text)
						if err != nil {
							cmn.DebugMsg(cmn.DbgLvlError, errMatchingSignature, err)
						} else if matched {
							updateDetectedTech(detectedTech, ObjName, signature.Confidence, signature.Content)
						}
					}
					updateDetectedType(detectedTech, ObjName, detectionType)
				}
			})
		}
	}
}

/*
func detectTechByMetaTagContent(text string, signature ruleset.MetaTag, ObjName string, detectedTech *map[string]detectionEntityDetails) {
	if signature.Content != "*" {
		detectTechByPrefix(text, strings.ToLower(signature.Content), ObjName, detectedTech, signature.Confidence)
		detectTechBySuffix(text, strings.ToLower(signature.Content), ObjName, detectedTech, signature.Confidence)
		detectTechByNegation(text, strings.ToLower(signature.Content), ObjName, detectedTech, signature.Confidence)
		detectTechByContains(text, strings.ToLower(signature.Content), ObjName, detectedTech, signature.Confidence)
	} else {
		updateDetectedTech(detectedTech, ObjName, signature.Confidence, "*")
	}
}
*/

func detectTechByURL(url string, URLSignatures *map[string][]ruleset.URLMicroSignature, detectedTech *map[string]detectionEntityDetails) {
	for ObjName := range *URLSignatures {
		for _, signature := range (*URLSignatures)[ObjName] {
			matched, err := regexp.MatchString(signature.Signature, url)
			if err != nil {
				cmn.DebugMsg(cmn.DbgLvlError, errMatchingSignature, err)
				continue
			}
			if matched {
				updateDetectedTech(detectedTech, ObjName, signature.Confidence, signature.Signature)
				updateDetectedType(detectedTech, ObjName, "url")
			}
		}
	}
}

// helper function to extract the domain from a URL
func urlToDomain(inputURL string) string {
	_, err := url.Parse(inputURL)
	if err != nil {
		return ""
	}

	// Given that url.Parse() does always extract a hostname correctly
	// we can safely ignore the error here
	h := urlToHost(inputURL)

	// Use EffectiveTLDPlusOne to correctly handle domains like "example.co.uk"
	domain, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		cmn.DebugMsg(cmn.DbgLvlError, "extracting domain from URL: %v", err)
		return ""
	}
	return domain
}

// helper function to extract the host from a URL
func urlToHost(url string) string {
	host := url
	if strings.Contains(host, "://") {
		host = host[strings.Index(host, "://")+3:]
	}
	if strings.Contains(host, "/") {
		host = host[:strings.Index(host, "/")]
	}
	host = strings.TrimSuffix(host, "/")
	host = strings.TrimSpace(host)
	return host
}
