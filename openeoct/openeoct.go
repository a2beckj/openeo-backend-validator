package main

import (
	"bytes"
	"context"
	"encoding/json"

	//"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Open-EO/openeo-backend-validator/openeoct/kin-openapi/openapi3"
	"github.com/Open-EO/openeo-backend-validator/openeoct/kin-openapi/openapi3filter"

	"github.com/BurntSushi/toml"
	"github.com/mcuadros/go-version"
	"github.com/urfave/cli"
)

// ErrorMessage "class"
type ErrorMessage struct {
	input  string
	output string
	msg    string
}

// Back end "class"
type BackEnd struct {
	url     string
	baseurl string
	version string

	// Add auth and that stuff
}

type WellKnown struct {
	versions []map[string]string
}

// CapEndpoints helper"class"
type CapEndpoint struct {
	Path    string
	Methods []string
}

// Capabilities helper"class"
type Capability struct {
	Endpoints []CapEndpoint
}

// Endpoint "class"
type Endpoint struct {
	Id           string
	Url          string
	Request_type string
	Body         string
	Header       string
	Optional     bool
	Group        string
	Timeout      int
	Wait         int
	RetryCode    string
	Order        int
	// Add auth and that stuff
}

// Endpoints sorting "class"
type ByOrder []Endpoint

func (a ByOrder) Len() int { return len(a) }
func (a ByOrder) Less(i, j int) bool {

	if a[i].Order == 0 {
		return false
	}

	if a[j].Order == 0 {
		return true
	}

	return a[i].Order < a[j].Order
}
func (a ByOrder) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

// ComplianceTest "class"
type ComplianceTest struct {
	backend      BackEnd
	apifile      string
	variables    map[string]string
	endpoints    map[string][]Endpoint
	authendpoint string
	username     string
	password     string
	output       string
	debug        bool
	router       *openapi3filter.Router
	capabilities Capability
}

// Elements of the Config file
type Config struct {
	Url            string
	Openapi        string
	Username       string
	Password       string
	Authurl        string
	Endpoints      map[string]Endpoint
	Output         string
	Config         string
	Variables      map[string]string
	Backendversion string
}

var CAP_EXCEPTIONS = map[string]bool{
	"/":                   true,
	"/.well-known/openeo": true,
}

func build_url(base string, ep string) string {
	u, _ := url.Parse(base)
	u.Path = path.Join(u.Path, ep)
	//log.Println(u.String())
	return u.String()
}

// Validates all enpoints defined in the compliance test instance.
// Returns a map of strings containing the states of the validation results
func (ct *ComplianceTest) validateAll() (map[string](map[string]string), *ErrorMessage) {

	states := make(map[string](map[string]string))

	token := ""

	authentication_err := new(ErrorMessage)

	// Set Authentication Token
	if ct.username != "" && ct.password != "" && ct.authendpoint != "" {

		client := &http.Client{}

		httpReq, _ := http.NewRequest(http.MethodGet, build_url(ct.backend.url, ct.authendpoint), nil)
		httpReq.SetBasicAuth(ct.username, ct.password)
		resp, errResp := client.Do(httpReq)

		if errResp != nil {
			authentication_err.input = build_url(ct.backend.url, ct.authendpoint)
			authentication_err.msg = "Error calling the authentication url! Wrong credentials?"
			authentication_err.output = string(errResp.Error())

		} else if resp.StatusCode == 200 {
			body, _ := ioutil.ReadAll(resp.Body)
			m := make(map[string]interface{})
			json.Unmarshal(body, &m)
			token, _ = m["access_token"].(string)
			authentication_err = nil
		} else {
			authentication_err.input = build_url(ct.backend.url, ct.authendpoint)
			authentication_err.msg = "Error calling the authentication url! Wrong credentials?"
			authentication_err.output = ""
		}
	} else {
		authentication_err = nil
	}

	for _, endpoints := range ct.endpoints {
		//Sorting within the group
		sort.Sort(ByOrder(endpoints))

		for _, endpoint := range endpoints {
			if (ct.checkCapability(endpoint) == false) && (!CAP_EXCEPTIONS[endpoint.Url]) {
				states[endpoint.Id] = make(map[string]string)
				states[endpoint.Id]["message"] = "Endpoint skipped, not listed in backend capabilities"
				states[endpoint.Id]["state"] = "NotSupported"
				//log.Println("Endpoint missing: " + endpoint.Id)
				continue
			}
			//log.Println("Group: " + group + ", Endpoint: " + endpoint.Id)
			endpoint.loadVariablesToEndpoint(*ct)
			counter := 0 // max tries are 10
			state, err := ct.validate(endpoint, token)
			if state == "Retry" {

				for counter < 10 {

					state, err = ct.validate(endpoint, token)
					if state != "Retry" {
						break
					}
					time.Sleep(2 * time.Second)
					counter++
				}
			}
			states[endpoint.Id] = make(map[string]string)
			states[endpoint.Id]["state"] = state

			if err != nil {
				if endpoint.Optional == false {
					states[endpoint.Id]["message"] = err.toString()
				} else {
					states[endpoint.Id]["message"] = "Non-mandatory endpoint, not supported by back-end"
					states[endpoint.Id]["state"] = "Valid"
				}
			} else {
				states[endpoint.Id]["message"] = ""
			}
			time.Sleep(time.Duration(endpoint.Wait) * time.Second)
		}
	}
	return states, authentication_err
}

func loadVariable(value string, variables map[string]string) string {
	var_name := GetStringInBetween(value, "{", "}")
	if var_name != "" {
		val, ok := variables[var_name]

		if ok == true {
			return strings.ReplaceAll(value, "{"+var_name+"}", val)
		} else {
			return value
		}
	}
	return value
}

func (ep *Endpoint) loadVariablesToEndpoint(ct ComplianceTest) {
	ep.Body = loadVariable(ep.Body, ct.variables)
	ep.Group = loadVariable(ep.Group, ct.variables)
	ep.Id = loadVariable(ep.Id, ct.variables)
	ep.Request_type = loadVariable(ep.Request_type, ct.variables)
	ep.Url = loadVariable(ep.Url, ct.variables)

}

func (err *ErrorMessage) toString() string {
	err_msg := err.output
	err_msg = strings.Replace(err_msg, "\n", "", -1)
	err_msg = strings.Replace(err_msg, "\"", "'", -1)
	space := regexp.MustCompile(`\s+`)
	err_msg = space.ReplaceAllString(err_msg, " ")
	err.output = err_msg
	return "Input: " + err.input + "; Error: " + err.msg + "; Details: " + err.output
}

func (ct *ComplianceTest) buildRequest(endpoint Endpoint, token string, abs_url bool) (*http.Request, *ErrorMessage) {

	method := http.MethodGet

	if endpoint.Request_type == "POST" {
		method = http.MethodPost
	} else if endpoint.Request_type == "PATCH" {
		method = http.MethodPatch
	} else if endpoint.Request_type == "PUT" {
		method = http.MethodPut
	} else if endpoint.Request_type == "DELETE" {
		method = http.MethodDelete
	}

	httpReq, _ := http.NewRequest(method, endpoint.Url, nil)

	if abs_url == true {
		if strings.Contains(endpoint.Url, ".well-known") {
			httpReq, _ = http.NewRequest(method, build_url(ct.backend.baseurl, endpoint.Url), nil)
			return httpReq, nil
		} else {
			httpReq, _ = http.NewRequest(method, build_url(ct.backend.url, endpoint.Url), nil)
		}
	}

	if token != "" {
		bearer := "Bearer basic//" + token
		httpReq.Header.Add("Authorization", bearer)
	}

	if _, err := os.Stat(endpoint.Body); err == nil {
		//httpReq.Header.Set("Content-Type", "application/json")
		dat, err := ioutil.ReadFile(endpoint.Body)
		if err != nil {
			errormsg := new(ErrorMessage)
			errormsg.input = endpoint.Id
			errormsg.msg = "Error loading body file: " + string(endpoint.Body)
			errormsg.output = string(err.Error())
			return httpReq, errormsg
		}

		stringReader := strings.NewReader(string(dat))
		stringReadCloser := ioutil.NopCloser(stringReader)
		httpReq.Body = stringReadCloser
		httpReq.ContentLength = int64(len(string(dat)))

	} else if os.IsNotExist(err) {
		// path/to/whatever does *not* exist
		if !(endpoint.Body == "") {
			errormsg := new(ErrorMessage)
			errormsg.input = endpoint.Id
			errormsg.msg = "Body was set in config file, but the file does not exist: " + endpoint.Body
			errormsg.output = string(err.Error())
			//log.Println(endpoint.Url, ": Body was set in config file, but the file does not exist: ", endpoint.Body)
			return httpReq, errormsg //fmt.Sprintf("%s: Body was set in config file, but the file does not exist: %s", endpoint.Url, endpoint.Body)
		}
	}

	// Set the correct request body content type according to the API
	if endpoint.Body != "" {
		// Find route in openAPI definition
		relhttpReq, _ := http.NewRequest(method, endpoint.Url, nil)
		route, _, errValue := ct.router.FindRoute(relhttpReq.Method, relhttpReq.URL)
		var content_types openapi3.Content
		//log.Println("route value: ", route)
		//log.Println("error value: ", errValue)
		if errValue != nil {
			errormsg := new(ErrorMessage)
			errormsg.input = endpoint.Id
			errormsg.msg = "Error setting correct content-type for url " + endpoint.Url + " and method " + endpoint.Request_type
			errormsg.output = string(errValue.Error())
			return httpReq, errormsg
		}
		if method == http.MethodPost {
			content_types = route.Swagger.Paths.Find(route.Path).Post.RequestBody.Value.Content
		} else if method == http.MethodPut {
			content_types = route.Swagger.Paths.Find(route.Path).Put.RequestBody.Value.Content
		} else if method == http.MethodPatch {
			content_types = route.Swagger.Paths.Find(route.Path).Patch.RequestBody.Value.Content
		} else if method == http.MethodDelete {
			content_types = route.Swagger.Paths.Find(route.Path).Delete.RequestBody.Value.Content
		}

		for content_type := range content_types {
			//log.Println(content_type)
			httpReq.Header.Set("Content-Type", content_type)
		}
	}

	return httpReq, nil

}

// Validates a single endpoint defined as input parameter.
// Returns the resulting state and an error message if something went wrong.
func (ct *ComplianceTest) validate(endpoint Endpoint, token string) (string, *ErrorMessage) {
	//log.Println(openapi3.SchemaStringFormats)
	//openapi3.DefineStringFormat("url", `^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	//log.Println(openapi3.SchemaStringFormats)

	if ct.debug == true {
		log.Println("====Endpoint " + endpoint.Id + "====")
	}

	if token != "" {
		if endpoint.Url == "/credentials/basic" {
			return "Valid", nil
		}
	}

	_, err := os.Stat(ct.apifile)

	// Try to read the openapi3 file
	swagger, err := openapi3.NewSwaggerLoader().LoadSwaggerFromFile(ct.apifile)

	if err != nil {
		// openapi3 file not found, assume it is an URI
		apiReq, _ := http.NewRequest(http.MethodGet, ct.apifile, nil)
		swagger, err = openapi3.NewSwaggerLoader().LoadSwaggerFromURI(apiReq.URL)
	}

	if err != nil {
		errormsg := new(ErrorMessage)
		errormsg.input = string(ct.apifile)
		errormsg.msg = "Error reading the openEO API, neighter file nor url found"
		errormsg.output = string(err.Error())
		return "Error", errormsg
	}

	router := openapi3filter.NewRouter().WithSwagger(swagger)
	ct.router = router
	ctx := context.TODO()

	// Define Local Request for validation
	httpReq, errReq := ct.buildRequest(endpoint, token, false)

	if errReq != nil {
		return "Error", errReq
	}

	// if ct.debug == true {
	// 	log.Println("---Request---")
	// 	log.Println("URL: ", string(httpReq.URL.RequestURI()))
	// 	log.Println("Method: ", httpReq.Method)
	// 	jsonString, _ := json.Marshal(httpReq.Header)
	// 	log.Println("Header: ", string(jsonString))
	// 	if httpReq.Body != nil {
	// 		reqbody, _ := ioutil.ReadAll(httpReq.Body)
	// 		log.Println("Body: ", string(reqbody))
	// 		stringReader := strings.NewReader(string(reqbody))
	// 		stringReadCloser := ioutil.NopCloser(stringReader)
	// 		httpReq.Body = stringReadCloser
	// 	} else {
	// 		log.Println("Body: Empty")
	// 	}
	// }

	// Find route in openAPI definition
	route, pathParams, err := router.FindRoute(httpReq.Method, httpReq.URL)

	if err != nil {
		errormsg := new(ErrorMessage)
		errormsg.input = string(httpReq.Method) + "  " + string(endpoint.Url)
		errormsg.msg = "Error finding endpoint in the OpenAPI definition"
		errormsg.output = err.Error()
		return "Invalid", errormsg
	}

	// Options for the validation
	options := &openapi3filter.Options{
		AuthenticationFunc: func(c context.Context, input *openapi3filter.AuthenticationInput) error {
			// TODO: support more schemes
			sec := input.SecurityScheme
			if sec.Type == "http" && sec.Scheme == "bearer" {
				if httpReq.Header.Get("Authorization") == "" {
					return nil //errors.New("Missing auth")
				}
			}
			return nil
		},
	}

	// Validate request
	requestValidationInput := &openapi3filter.RequestValidationInput{
		Request:    httpReq,
		PathParams: pathParams,
		Route:      route,
		Options:    options}

	if err := openapi3filter.ValidateRequest(ctx, requestValidationInput); err != nil {
		errormsg := new(ErrorMessage)
		errormsg.input = string(httpReq.Method) + "  " + string(endpoint.Url)
		errormsg.msg = "Error validating the request"
		errormsg.output = string(err.Error())
		return "Invalid", errormsg
	}

	// Send request
	client := &http.Client{}

	// Set timeout if given
	if endpoint.Timeout != 0 {
		client.Timeout = time.Duration(endpoint.Timeout) * time.Second
	}

	// execReq, errReq := httpReq, err ct.buildRequest(endpoint, token, true)
	execReq, errReq := ct.buildRequest(endpoint, token, true)

	if ct.debug == true {
		log.Println("---Request---")
		log.Println("URL: ", string(execReq.URL.RequestURI()))
		log.Println("Method: ", execReq.Method)
		jsonString, _ := json.Marshal(execReq.Header)
		log.Println("Header: ", string(jsonString))
		if execReq.Body != nil {
			reqbody, _ := ioutil.ReadAll(execReq.Body)
			log.Println("Body: ", string(reqbody))
			stringReader := strings.NewReader(string(reqbody))
			stringReadCloser := ioutil.NopCloser(stringReader)
			execReq.Body = stringReadCloser
		} else {
			log.Println("Body: Empty")
		}
	}

	if errReq != nil {
		return "Error", errReq
	}

	resp, err := client.Do(execReq)

	if err != nil {
		errormsg := new(ErrorMessage)
		errormsg.input = string(execReq.Method) + "  " + string(endpoint.Url)
		errormsg.msg = "Error sending request to back end"
		errormsg.output = string(err.Error())
		return "Invalid", errormsg
	}

	// Get Response
	body, err := ioutil.ReadAll(resp.Body)

	if ct.debug == true {
		log.Println("---Response---")
		log.Println("Status Code: ", resp.StatusCode)
		jsonString, _ := json.Marshal(resp.Header)
		log.Println("Header: ", string(jsonString))
		if body != nil {
			//reqbody, _ := ioutil.ReadAll(httpReq.Body)
			if len(body) < 1000 {
				log.Printf("Body (length %d): %s\n", len(body), body)
			} else {
				log.Printf("Body (length %d): %q...\n", len(body), body[:1000])
			}
		} else {
			log.Println("Body: Empty")
		}
	}

	// log.Println(string(body))

	if resp.StatusCode == 401 {
		errormsg := new(ErrorMessage)
		errormsg.input = "Header Auth: " + execReq.Header.Get("Authorization")
		errormsg.msg = "Error: Basic Authentication failed."
		errormsg.output = string(body)
		return "Invalid", errormsg
	}

	if err != nil {
		errormsg := new(ErrorMessage)
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		//errormsg.input = "Response body: " + buf.String()
		errormsg.msg = "Error reading response from the back end"
		errormsg.output = string(err.Error())
		return "Invalid", errormsg
	}

	if resp.StatusCode == 404 {
		errormsg := new(ErrorMessage)
		errormsg.input = endpoint.Url
		errormsg.msg = "Response Code " + strconv.Itoa(resp.StatusCode)
		errormsg.output = string(body)
		return "Missing", errormsg
	} else if resp.StatusCode >= 400 && resp.StatusCode < 600 {
		errormsg := new(ErrorMessage)
		errormsg.input = endpoint.Url
		errormsg.msg = "Response Code " + strconv.Itoa(resp.StatusCode)
		errormsg.output = string(body)
		if endpoint.RetryCode != "" {
			if strings.Contains(string(body), endpoint.RetryCode) {
				//log.Println(endpoint.RetryCode)
				return "Retry", errormsg
			}
		}
		return "Error", errormsg
	}

	var (
		respStatus = resp.StatusCode
		respHeader = resp.Header
		respBody   = string(body)
	)

	// Define validation input
	responseValidationInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: requestValidationInput,
		Status:                 respStatus,
		Header:                 respHeader}

	if respBody != "" {
		responseValidationInput.SetBodyBytes([]byte(respBody))
	}

	// Validate response.
	if err := openapi3filter.ValidateResponse(ctx, responseValidationInput); err != nil {
		errormsg := new(ErrorMessage)
		//errormsg.input = "Response Body: " + string(body)
		errormsg.msg = "Response of the back end not valid"
		errormsg.output = err.Error()
		return "Invalid", errormsg
	}

	//Set Job Id in the compliance test instance
	if endpoint.Url == "/jobs" && endpoint.Request_type == "POST" {
		if resp.Header.Get("OpenEO-Identifier") != "" {
			ct.variables["job_id"] = resp.Header.Get("OpenEO-Identifier")
		} else {
			log.Println("Warning: Not able to catch the job_id from POST /jobs header via 'OpenEO-Identifier' or empty!")
		}
	}

	//Set Service Id in the compliance test instance
	if endpoint.Url == "/services" && endpoint.Request_type == "POST" {
		if resp.Header.Get("OpenEO-Identifier") != "" {
			ct.variables["service_id"] = resp.Header.Get("OpenEO-Identifier")
		} else {
			log.Println("Warning: Not able to catch the service_id from POST /services header via 'OpenEO-Identifier' or empty!")
		}
	}

	return "Valid", nil
}

// Reads info from config file
func ReadConfig(config_file string) Config {
	var configfile = config_file
	var config Config

	// Check if file exists
	_, err := os.Stat(configfile)
	if err != nil {
		log.Fatal("Config file is missing: ", configfile)
	}

	// Read file if TOML File

	if _, err := toml.DecodeFile(configfile, &config); err != nil {

		//Read file as JSON File
		data, err2 := ioutil.ReadFile(config_file)

		if err2 != nil {
			log.Println("Error reading Config file as TOML: ", err)
			log.Fatal("Error reading Config file as JSON: ", err2)
		}
		err2 = json.Unmarshal(data, &config)
		if err2 != nil {
			log.Println("Error reading Config file as TOML: ", err)
			log.Fatal("Error reading Config file as JSON:", err2)
		}
	}

	return config
}

// Reads info from config file
func ReturnConfigValue(config_item string) string {
	var config_value = config_item
	if strings.HasPrefix(config_value, "$") {
		env_var := strings.Replace(config_value, "$", "", 1)
		config_value = os.Getenv(env_var)
		if len(config_value) == 0 {
			log.Println("Warning: Environment variable does not exist or is empty:", string(env_var))
			log.Println("Warning: Used raw input instead:", string(config_item))
			return config_item
		}
	}
	return config_value
}

// GetStringInBetween Returns empty string if no start string found
func GetStringInBetween(str string, start string, end string) (result string) {
	s := strings.Index(str, start)
	if s == -1 {
		return
	}
	s += len(start)
	e := strings.Index(str, end)
	if e == -1 {
		return
	}
	return str[s:e]
}

func (be *BackEnd) loadUrl() {
	if be.version != "" {

		// Get backend version
		well_known := be.baseurl + "/.well-known/openeo"
		client := &http.Client{}
		httpReq, _ := http.NewRequest(http.MethodGet, well_known, nil)
		resp, errResp := client.Do(httpReq)

		if errResp != nil {
			log.Println("Warning: Failed to get backend version url from .wellknown : ", well_known, errResp)
			log.Println("Warning: Setting URL to base url: ", be.baseurl)
			be.url = be.baseurl
		} else {
			// Get Response
			wellknown := make(map[string]([]map[string]string))

			body, _ := ioutil.ReadAll(resp.Body)

			json.Unmarshal(body, &wellknown)
			be_version := version.Normalize(be.version)
			for val := range wellknown["versions"] {

				wellknown_version := version.Normalize(wellknown["versions"][val]["api_version"])

				if be_version == wellknown_version {
					be.url = wellknown["versions"][val]["url"]
				}
			}
			if be.url == "" {
				log.Println("Warning: Given backend version <"+be.version+"> was not found, using just url: ", be.baseurl)
				be.url = be.baseurl
			}
		}

	} else {
		be.url = be.baseurl
	}

}

func (ct *ComplianceTest) checkCapability(ep Endpoint) bool {

	if len(ct.capabilities.Endpoints) == 0 {
		return true
	}

	//log.Println("Start endpoint: " + ep.Url + " Method:" + ep.Request_type)
	for _, cap_ep := range ct.capabilities.Endpoints {
		re := regexp.MustCompile(`^` + cap_ep.Path + `$`)
		if re.MatchString(ep.Url) {
			//if ep.Url != "/" && cap_ep.Path == "/" {
			//	continue
			//}
			//	log.Println("URL Match: " + ep.Url + " Regex: " + cap_ep.Path)
			for _, met := range cap_ep.Methods {
				//log.Println("Compare Method: " + ep.Request_type + " Regex: " + met)
				if met == ep.Request_type {
					return true
				}
			}
		} else {
			//log.Println("Not a match: ^" + cap_ep.Path + "$ Regex with " + ep.Url)
		}
	}
	//log.Println("Endpoint not found: " + ep.Url + " Method:" + ep.Request_type)
	return false
}
func (ct *ComplianceTest) loadCapabilities() {

	// Get backend capabilities

	if ct.backend.url == "" {
		return
	}

	capa_url := build_url(ct.backend.url, "/")
	// log.Println(capa_url)
	client := &http.Client{}
	httpReq, _ := http.NewRequest(http.MethodGet, capa_url, nil)
	resp, err := client.Do(httpReq)

	//	buf := new(bytes.Buffer)
	//	buf.ReadFrom(resp.Body)
	//	newStr := buf.String()
	//	log.Println(newStr)

	// buf, err := os.Open("examples/eodc_main_capabilities.txt")

	if err != nil {
		return
	}

	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()

	// buf := new(bytes.Buffer)
	// buf.ReadFrom(resp.Body)
	// newStr := buf.String()

	var capa Capability
	dec.Decode(&capa)

	r := regexp.MustCompile(`{[^{}]*}`)
	//log.Println(capa)

	for i, _ := range capa.Endpoints {
		//log.Println(cap_ep.Path)
		//log.Println(r.ReplaceAllLiteralString(cap_ep.Path, `(.*)`))
		capa.Endpoints[i].Path = r.ReplaceAllLiteralString(capa.Endpoints[i].Path, `[^/]*`)
	}

	ct.capabilities = capa

}

func (ct *ComplianceTest) appendConfig(config Config) {

	if config.Config != "" {
		config_ext := ReadConfig(config.Config)
		ct.appendConfig(config_ext)
	}

	if ct.variables == nil {
		ct.variables = make(map[string]string)
	}

	if config.Variables != nil {
		for name, value := range config.Variables {
			ct.variables[name] = ReturnConfigValue(value)
		}
		// ct.variables = config.Variables
	}

	// for name, ep := range ct.variables {
	// 	log.Println(name + " -- " + ep)
	// }
	if config.Url != "" {
		ct.backend.baseurl = ReturnConfigValue(config.Url)
	}

	if config.Output != "" {
		ct.output = ReturnConfigValue(config.Output)
	}

	if config.Backendversion != "" {
		ct.backend.version = ReturnConfigValue(config.Backendversion)
	}

	ct.backend.loadUrl()

	if config.Openapi != "" {
		ct.apifile = ReturnConfigValue(config.Openapi)
	}

	if config.Username != "" {
		ct.username = ReturnConfigValue(config.Username)
	}
	if config.Password != "" {
		ct.password = ReturnConfigValue(config.Password)
	}
	// ct.authendpoint = ReturnConfigValue(config.Authurl)

	if config.Authurl == "" {
		ct.authendpoint = "/credentials/basic"
	} else {
		ct.authendpoint = ReturnConfigValue(config.Authurl)
	}

	if config.Password != "" {
		ct.password = ReturnConfigValue(config.Password)
	}

	if config.Endpoints != nil {
		var ep_groups map[string][]Endpoint
		ep_groups = make(map[string][]Endpoint)
		for name, ep := range config.Endpoints {
			//log.Println("Ep:", string(ep))
			if ep.Id == "" {
				name_split := strings.Split(name, ".")
				ep.Id = name_split[len(name_split)-1]
			}

			if ep.Group == "" {
				ep.Group = "nogroup"
			}

			ep_groups[ep.Group] = append(ep_groups[ep.Group], ep)
		}
		// merge with existing ones
		if ct.endpoints != nil {
			for name, group := range ct.endpoints {

				for _, ep := range group {
					//log.Println(ep)
					if ep.Id == "" {
						name_split := strings.Split(name, ".")
						ep.Id = name_split[len(name_split)-1]
					}

					ep_groups[name] = append(ep_groups[name], ep)
				}

			}
		}

		ct.endpoints = ep_groups
	}

	ct.loadCapabilities()
}

// Main function
func main() {
	start_time := time.Now()
	// Config file path
	//var config Config
	//var config_ep Config

	ct := new(ComplianceTest)

	// CLI handling
	app := cli.NewApp()
	app.Name = "openeoct"
	app.Name = "openeoct"
	app.Version = "1.0.0"
	app.Usage = "validating a back end against an openapi description file!"

	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "activate debug info",
		},
	}
	// add config command
	app.Commands = []*cli.Command{
		{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "load from config file",
			Action: func(c *cli.Context) error {
				//configfile = c.Args().First()
				for i := 0; i < c.Args().Len(); i++ {
					ct.appendConfig(ReadConfig(c.Args().Get(i)))

				}
				if c.Bool("debug") {
					ct.debug = true
				}
				//log.Println("Configfile1: ", config.Url)
				return nil
			},
		},
	}

	// run CLI
	apperr := app.Run(os.Args)
	if apperr != nil {
		log.Fatal(apperr)
	}

	//ct.debug = true
	//ct.appendConfig(ReadConfig("examples/gee_config_v1_0_0_external.toml"))
	//ct.appendConfig(ReadConfig("examples/D28/openeo_v1.0_endpoints.toml"))
	//ct.appendConfig(ReadConfig("examples/D28/GEE_config.toml"))

	//ct.debug = true
	//ct.appendConfig(ReadConfig("examples/eodc_config_v1_0.toml"))
	// ct.appendConfig(ReadConfig(c.Args().Get(i)))
	//config = ReadConfig("examples/gee_config_v1_0.json")
	//config = ReadConfig("examples/gee_config_v1_0_0_external.toml")
	//config = ReadConfig("examples/eodc_config_v1_0.toml")

	//config = ReadConfig("examples/gee_config_v1_0_0_external.toml")
	// define back end and compliance test instance

	//ct.appendConfig(config)
	//ct.appendConfig(config_ep)

	// config file read correctly
	if ct.backend.url == "" {
		log.Fatal("Error: No config file or backend url specified")
	}

	// Run validation
	result, err := ct.validateAll()

	if err != nil {
		log.Println(err.toString())
	}

	end_time := time.Now()

	var result_json map[string](map[string](map[string]interface{}))
	result_json = make(map[string](map[string](map[string]interface{})))
	result_json["result"] = make(map[string](map[string]interface{}))
	result_json["stats"] = make(map[string](map[string]interface{}))
	result_json["stats"]["backend"] = make(map[string]interface{})
	result_json["stats"]["execution"] = make(map[string]interface{})
	result_json["stats"]["spec"] = make(map[string]interface{})
	result_json["stats"]["backend"]["url"] = ct.backend.url
	result_json["stats"]["backend"]["baseurl"] = ct.backend.baseurl
	result_json["stats"]["backend"]["version"] = ct.backend.version
	result_json["stats"]["execution"]["start"] = start_time.Format("2006-01-02 15:04:05")
	result_json["stats"]["execution"]["end"] = end_time.Format("2006-01-02 15:04:05")
	result_json["stats"]["spec"]["apifile"] = ct.apifile

	for group, endpoints := range ct.endpoints {
		for _, ep := range endpoints {
			ep.loadVariablesToEndpoint(*ct)
			if result_json["result"][group] == nil {
				result_json["result"][group] = make(map[string]interface{})
				result_json["result"][group]["group_summary"] = ""
				result_json["result"][group]["endpoints"] = make(map[string](map[string]string))
			}

			result_json["result"][group]["endpoints"].(map[string](map[string]string))[ep.Id] = result[ep.Id]
			result_json["result"][group]["endpoints"].(map[string](map[string]string))[ep.Id]["url"] = ep.Url
			result_json["result"][group]["endpoints"].(map[string](map[string]string))[ep.Id]["type"] = ep.Request_type
			if result[ep.Id]["state"] != "Valid" && result[ep.Id]["state"] != "NotSupported" {
				result_json["result"][group]["group_summary"] = "Invalid"
			} else if result[ep.Id]["state"] == "Valid" {
				if result_json["result"][group]["group_summary"] != "Invalid" {
					result_json["result"][group]["group_summary"] = "Valid"
				}
			} else if result[ep.Id]["state"] == "NotSupported" && result_json["result"][group]["group_summary"] == "" {
				result_json["result"][group]["group_summary"] = "NotSupported"
			}
		}
	}

	jsonString, _ := json.MarshalIndent(result_json, "", "    ")

	output := ReturnConfigValue(ct.output)

	// Write to log stdout or to output file
	if output == "" {
		log.Println(string(jsonString))
	} else {
		ioutil.WriteFile(output, jsonString, 0644)
	}

}
