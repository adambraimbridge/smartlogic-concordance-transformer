package smartlogic

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Financial-Times/kafka-client-go/kafka"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"errors"
)

const (
	ExpectedContentType = "application/json"
	WRITER_ADDRESS      = "http://localhost:8080/__concordance-rw-dynamodb/"
	TOPIC               = "TestTopic"
)

type mockHttpClient struct {
	resp       string
	statusCode int
	err        error
}

func TestAdminHandler_Healthy(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 200}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterAdminHandlers(r)

	type testStruct struct {
		endpoint           string
		expectedStatusCode int
		expectedBody       string
		expectedError      string
	}

	buildInfoChecker := testStruct{endpoint: "/__build-info", expectedStatusCode: 200, expectedBody: "Version  is not a semantic version"}
	gtgChecker := testStruct{endpoint: "/__gtg", expectedStatusCode: 200, expectedBody: ""}
	healthChecker := testStruct{endpoint: "/__health", expectedStatusCode: 200, expectedBody: ""}

	testScenarios := []testStruct{buildInfoChecker, gtgChecker, healthChecker}

	for _, scenario := range testScenarios {
		rec := httptest.NewRecorder()
		http.DefaultServeMux.ServeHTTP(rec, newRequest("GET", scenario.endpoint, ""))
		assert.Equal(t, scenario.expectedStatusCode, rec.Code)
		assert.Contains(t, rec.Body.String(), scenario.expectedBody)
	}

}

func TestProcessKafkaMessage(t *testing.T) {
	mockClient := mockHttpClient{resp: "", statusCode: 200}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})

	type testStruct struct {
		scenarioName       string
		payload            kafka.FTMessage
		expectedError      error
	}

	invalidJsonLd := `{"@graph": [{"@id": "http://www.ft.com/thing/20db1bd6-59f9-4404-adb5-3165a448f8b0"}, {"@id": "http://www.ft.com/thing/20db1bd6-59f9-4404-adb5-3165a448f8b0"}]}`
	validJsonLdNoConcordance := `{"@graph": [{"@id": "http://www.ft.com/thing/20db1bd6-59f9-4404-adb5-3165a448f8b0"}]}`
	validJsonLdWithConcordance := `{"@graph": [{"@id": "http://www.ft.com/thing/20db1bd6-59f9-4404-adb5-3165a448f8b0","http://www.ft.com/ontology/TMEIdentifier": [{"@value": "AbCdEfgHiJkLMnOpQrStUvWxYz-0123456789"}]}]}`

	failOnInvalidKafkaMessagePayload := testStruct{scenarioName: "failOnInvalidKafkaMessagePayload", payload: kafka.FTMessage{Body: ""},	expectedError: errors.New("EOF")}
	failOnInvalidJsonLdInPayload := testStruct{scenarioName: "failOnInvalidJsonLdInPayload", payload: kafka.FTMessage{Body: invalidJsonLd, Headers: map[string]string{"X-Request-Id":"test_tid"}}, expectedError: errors.New("Invalid Request Json: More than 1 concept in smartlogic concept payload which is currently not supported")}
	failOnWritePayloadToWriter := testStruct{scenarioName: "failOnWritePayloadToWriter", payload: kafka.FTMessage{Body: validJsonLdNoConcordance, Headers: map[string]string{"X-Request-Id":"test_tid"}}, expectedError: errors.New("Internal Error: Delete request to writer returned unexpected status: 200")}
	successfulRequest := testStruct{scenarioName: "successfulRequest", payload: kafka.FTMessage{Body: validJsonLdWithConcordance, Headers: map[string]string{"X-Request-Id":"test_tid"}}, expectedError: nil}

	scenarios := []testStruct{failOnInvalidKafkaMessagePayload, failOnInvalidJsonLdInPayload, failOnWritePayloadToWriter, successfulRequest}

	for _, scenario := range scenarios {
		err := h.ProcessKafkaMessage(scenario.payload)
		assert.Equal(t, scenario.expectedError, err, "Scenario " + scenario.scenarioName + " failed with unexpected error")
	}

}

func TestTransformAndSendHandlers(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 200}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterHandlers(r)

	type testStruct struct {
		scenarioName       string
		filePath           string
		endpoint           string
		expectedStatusCode int
		expectedResult     string
	}

	transform_unprocessibleEntityError := testStruct{scenarioName: "transform_unprocessibleEntityError", filePath: "../resources/sourceJson/multipleGraphsInList.json", endpoint: "/transform", expectedStatusCode: 422, expectedResult: "Invalid Request Json: More than 1 concept in smartlogic concept payload which is currently not supported"}
	transform_convertingToConcordedJsonError := testStruct{scenarioName: "transform_convertingToConcordedJsonError", filePath: "../resources/sourceJson/invalidTmeId.json", endpoint: "/transform", expectedStatusCode: 400, expectedResult: "is not a valid TME Id"}
	transform_duplicateTmeIdsError := testStruct{scenarioName: "transform_duplicateTmeIdsError", filePath: "../resources/sourceJson/duplicateTmeIds.json", endpoint: "/transform", expectedStatusCode: 400, expectedResult: "contains duplicate TME id values"}
	transform_convertsAndReturnsPayload := testStruct{scenarioName: "transform_convertsAndReturnsPayload", filePath: "../resources/sourceJson/multipleTmeIds.json", endpoint: "/transform", expectedStatusCode: 200, expectedResult: "{\"uuid\":\"20db1bd6-59f9-4404-adb5-3165a448f8b0\",\"concordedIds\":[\"e9f4525a-401f-3b23-a68e-e48f314cdce6\",\"83f63c7e-1641-3c7b-81e4-378ae3c6c2ad\",\"e4bc4ac2-0637-3a27-86b1-9589fca6bf2c\",\"e574b21d-9abc-3d82-a6c0-3e08c85181bf\"]}"}
	send_unprocessibleEntityError := testStruct{scenarioName: "send_unprocessibleEntityError", filePath: "../resources/sourceJson/multipleGraphsInList.json", endpoint: "/transform/send", expectedStatusCode: 422, expectedResult: "Invalid Request Json: More than 1 concept in smartlogic concept payload which is currently not supported"}
	send_convertingToConcordedJsonError := testStruct{scenarioName: "send_convertingToConcordedJsonError", filePath: "../resources/sourceJson/invalidTmeId.json", endpoint: "/transform/send", expectedStatusCode: 400, expectedResult: "is not a valid TME Id"}
	send_convertsAndForwardsPayloadWithConcordance := testStruct{scenarioName: "send_convertsAndForwardsPayloadWithConcordance", filePath: "../resources/sourceJson/multipleTmeIds.json", endpoint: "/transform/send", expectedStatusCode: 200, expectedResult: "{\"message\":\"Concordance record forwarded to writer\"}"}
	send_convertsAndFailsForwardToRw := testStruct{scenarioName: "send_convertsAndFailsForwardToRw", filePath: "../resources/sourceJson/noTmeIds.json", endpoint: "/transform/send", expectedStatusCode: 500, expectedResult: "Internal Error: Delete request to writer returned unexpected status:"}

	testScenarios := []testStruct{transform_unprocessibleEntityError, transform_convertingToConcordedJsonError, transform_duplicateTmeIdsError, transform_convertsAndReturnsPayload, send_unprocessibleEntityError, send_convertingToConcordedJsonError, send_convertsAndForwardsPayloadWithConcordance, send_convertsAndFailsForwardToRw}

	for _, scenario := range testScenarios {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, newRequest("POST", scenario.endpoint, readFile(t, scenario.filePath)))
		assert.Equal(t, scenario.expectedStatusCode, rec.Code, scenario.scenarioName)
		assert.Equal(t, rec.HeaderMap["Content-Type"], []string{"application/json"}, scenario.scenarioName)
		assert.Contains(t, rec.Body.String(), scenario.expectedResult, "Failed scenario: "+scenario.scenarioName)
	}

}

func TestSendHandlerSuccessfulDelete(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 404}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterHandlers(r)


	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newRequest("POST", "/transform/send", readFile(t, "../resources/sourceJson/noTmeIds.json")))
	assert.Equal(t, 200, rec.Code, "Unexpected status code")
	assert.Equal(t, rec.HeaderMap["Content-Type"], []string{"application/json"}, "Unexpected Content-Type")
	assert.Contains(t, rec.Body.String(), "Concordance record not found", "Request had unexpected result")
}

func TestSendHandlerRecordNotFound(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 204}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterHandlers(r)


	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newRequest("POST", "/transform/send", readFile(t, "../resources/sourceJson/noTmeIds.json")))
	assert.Equal(t, 200, rec.Code, "Unexpected status code")
	assert.Equal(t, rec.HeaderMap["Content-Type"], []string{"application/json"}, "Unexpected Content-Type")
	assert.Contains(t, rec.Body.String(), "Concordance record successfuly deleted", "Request had unexpected result")
}

func TestSendHandlerUnavailableWriter(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 503}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterHandlers(r)


	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newRequest("POST", "/transform/send", readFile(t, "../resources/sourceJson/noTmeIds.json")))
	assert.Equal(t, 500, rec.Code, "Unexpected status code")
	assert.Equal(t, rec.HeaderMap["Content-Type"], []string{"application/json"}, "Unexpected Content-Type")
	assert.Contains(t, rec.Body.String(), "Delete request to writer returned unexpected status: 503", "Request had unexpected result")
}

func TestSendHandlerWriteReturnsError(t *testing.T) {
	r := mux.NewRouter()
	mockClient := mockHttpClient{resp: "", statusCode: 503, err: errors.New("Delete request to writer returned unexpected status: 503")}
	defaultTransformer := NewTransformerService(TOPIC, WRITER_ADDRESS, &mockClient)
	h := NewHandler(defaultTransformer, mockConsumer{})
	h.RegisterHandlers(r)


	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newRequest("POST", "/transform/send", readFile(t, "../resources/sourceJson/noTmeIds.json")))
	assert.Equal(t, 503, rec.Code, "Unexpected status code")
	assert.Equal(t, rec.HeaderMap["Content-Type"], []string{"application/json"}, "Unexpected Content-Type")
	assert.Contains(t, rec.Body.String(), "Delete request to writer returned unexpected status: 503", "Request had unexpected result")
}

func (c mockHttpClient) Do(req *http.Request) (resp *http.Response, err error) {
	cb := ioutil.NopCloser(bytes.NewReader([]byte(c.resp)))
	return &http.Response{Body: cb, StatusCode: c.statusCode}, c.err
}

type mockConsumer struct {
	err error
}

func (mc mockConsumer) ConnectivityCheck() error {
	return mc.err
}

func (mc mockConsumer) StartListening(messageHandler func(message kafka.FTMessage) error) {
	return
}

func (mc mockConsumer) Shutdown() {
	return
}

func newRequest(method, url string, body string) *http.Request {
	var payload io.Reader
	if body != "" {
		payload = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, url, payload)
	req.Header = map[string][]string{
		"Content-Type": {ExpectedContentType},
	}
	if err != nil {
		panic(err)
	}
	return req
}