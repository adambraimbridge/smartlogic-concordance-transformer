package smartlogic

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"fmt"
	"github.com/Financial-Times/go-fthealth"
	"github.com/Financial-Times/http-handlers-go/httphandlers"
	"github.com/Financial-Times/kafka-client-go/kafka"
	serviceStatus "github.com/Financial-Times/service-status-go/httphandlers"
	"github.com/Financial-Times/transactionid-utils-go"
	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/rcrowley/go-metrics"
)

type SmartlogicConcordanceTransformerHandler struct {
	transformer TransformerService
	consumer    kafka.Consumer
}

func NewHandler(transformer TransformerService, consumer kafka.Consumer) SmartlogicConcordanceTransformerHandler {
	return SmartlogicConcordanceTransformerHandler{
		transformer: transformer,
		consumer:    consumer,
	}
}

func (h *SmartlogicConcordanceTransformerHandler) ProcessKafkaMessage(msg kafka.FTMessage) error {
	return h.transformer.handleConcordanceEvent(msg.Body, msg.Headers["X-Request-Id"])
}

func (h *SmartlogicConcordanceTransformerHandler) TransformHandler(rw http.ResponseWriter, req *http.Request) {
	tid := transactionidutils.GetTransactionIDFromRequest(req)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("X-Request-Id", tid)

	var smartLogicConcept = SmartlogicConcept{}
	err := json.NewDecoder(req.Body).Decode(&smartLogicConcept)

	if err != nil {
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": smartLogicConcept.Concepts[0].Id}).Error("Error whilst processing request body")
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("{\"message\":\"Error whilst processing request body: " + err.Error() + "\"}"))
		return
	}

	updateStatus, conceptUuid, uppConcordance, err := convertToUppConcordance(smartLogicConcept)
	log.WithFields(log.Fields{"trans_id": tid, "UUID": conceptUuid}).Debug("Processing concordance transformation")

	defer req.Body.Close()

	writeResponse(rw, updateStatus, err, uppConcordance, tid)
	return
}

func (h *SmartlogicConcordanceTransformerHandler) SendHandler(rw http.ResponseWriter, req *http.Request) {
	tid := transactionidutils.GetTransactionIDFromRequest(req)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("X-Request-Id", tid)

	var smartLogicConcept = SmartlogicConcept{}
	err := json.NewDecoder(req.Body).Decode(&smartLogicConcept)

	if err != nil {
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": smartLogicConcept.Concepts[0].Id}).Error("Error whilst processing request body")
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("{\"message\":\"Error whilst processing request body:" + err.Error() + "\"}"))
		return
	}

	updateStatus, conceptUuid, uppConcordance, err := convertToUppConcordance(smartLogicConcept)
	if err != nil {
		writeResponse(rw, updateStatus, err, uppConcordance, tid)
	}

	updateStatus, err = h.transformer.makeRelevantRequest(conceptUuid, uppConcordance, tid)

	if err != nil {
		writeResponse(rw, updateStatus, err, uppConcordance, tid)
		return
	}

	rw.WriteHeader(http.StatusOK)
	rw.Write([]byte("{\"message\":\"Concordance record for " + conceptUuid + " forwarded to writer\"}"))

	defer req.Body.Close()
	return
}

func writeResponse(rw http.ResponseWriter, updateStatus status, err error, concordance UppConcordance, tid string) {
	enc := json.NewEncoder(rw)
	if err == nil {
		rw.WriteHeader(http.StatusOK)
		bytes, _ := json.Marshal(concordance)
		rw.Write(bytes)
		log.WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Info("Served concordance request")
		return
	}
	switch updateStatus {
	case VALID_CONCEPT:
		if err := enc.Encode(concordance); err != nil {
			log.WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Error("Unknown backend error")
			writeJSONError(rw, err.Error(), http.StatusServiceUnavailable)
			return
		} else {
			fmt.Printf("Can we actually get here?\n")
			rw.WriteHeader(int(updateStatus))
			rw.Write([]byte(""))
		}
	case SYNTACTICALLY_INCORRECT:
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Error("Bad request")
		writeJSONError(rw, err.Error(), http.StatusBadRequest)
		return
	case SEMANTICALLY_INCORRECT:
		writeJSONError(rw, err.Error(), http.StatusUnprocessableEntity)
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Error("Bad json")
		return
	case DELETED_CONCEPT:
		writeJSONError(rw, "Concordance request forwarded to writer", http.StatusNoContent)
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Error("Concordance record doesnt exist")
		return
	default:
		writeJSONError(rw, err.Error(), http.StatusServiceUnavailable)
		log.WithError(err).WithFields(log.Fields{"trans_id": tid, "UUID": concordance.ConceptUuid, "status": int(updateStatus)}).Error("Unknown backend error")
		return
	}
}

func writeJSONError(w http.ResponseWriter, errorMsg string, statusCode int) {
	w.WriteHeader(statusCode)
	fmt.Fprintln(w, fmt.Sprintf("{\"message\": \"%s\"}", errorMsg))
}

func (h *SmartlogicConcordanceTransformerHandler) gtgCheck(rw http.ResponseWriter, req *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	if err := h.checkKafkaConnectivity(); err != nil {
		log.Errorf("Kafka Healthcheck failed; %v", err.Error())
		rw.WriteHeader(http.StatusServiceUnavailable)
		rw.Write([]byte("Kafka healthcheck failed"))
		return
	}
	if err := h.checkConcordanceRwConnectivity(); err != nil {
		log.Errorf("Concordance Rw Dynamodb Healthcheck failed; %v", err.Error())
		rw.WriteHeader(http.StatusServiceUnavailable)
		rw.Write([]byte("Concordance Rw Dynamodb healthcheck failed"))
		return
	}
	rw.WriteHeader(http.StatusOK)
}

func (h *SmartlogicConcordanceTransformerHandler) RegisterHandlers(router *mux.Router) {
	log.Info("Registering handlers")
	transformAndWrite := handlers.MethodHandler{
		"POST": http.HandlerFunc(h.SendHandler),
	}
	router.Handle("/transform/send", transformAndWrite)
	transformAndReturn := handlers.MethodHandler{
		"POST": http.HandlerFunc(h.TransformHandler),
	}
	router.Handle("/transform", transformAndReturn)
}

func (h *SmartlogicConcordanceTransformerHandler) RegisterAdminHandlers(router *mux.Router) {
	log.Info("Registering admin handlers")

	var monitoringRouter http.Handler = router
	monitoringRouter = httphandlers.TransactionAwareRequestLoggingHandler(log.StandardLogger(), monitoringRouter)
	monitoringRouter = httphandlers.HTTPMetricsHandler(metrics.DefaultRegistry, monitoringRouter)

	var checks []fthealth.Check = []fthealth.Check{h.concordanceRwDynamoDbHealthCheck(), h.kafkaHealthCheck()}
	http.HandleFunc("/__health", fthealth.Handler("ConceptIngester Healthchecks", "Checks for accessing writer", checks...))
	http.HandleFunc("/__gtg", h.gtgCheck)
	http.HandleFunc("/__ping", serviceStatus.PingHandler)
	http.HandleFunc("/__build-info", serviceStatus.BuildInfoHandler)
	http.Handle("/", monitoringRouter)
}

func (h *SmartlogicConcordanceTransformerHandler) kafkaHealthCheck() fthealth.Check {
	return fthealth.Check{
		BusinessImpact:   "Editorial updates of concpet concordances will not be written into UPP",
		Name:             "Check connectivity to Kafka",
		PanicGuide:       "https://dewey.ft.com/smartlogic-concordance-transform.html",
		Severity:         3,
		TechnicalSummary: `Check that kafka and zookeeper are healthy in this cluster; if so restart this service`,
		Checker:          h.checkKafkaConnectivity,
	}
}

func (h *SmartlogicConcordanceTransformerHandler) concordanceRwDynamoDbHealthCheck() fthealth.Check {
	return fthealth.Check{
		BusinessImpact:   "Editorial updates of concpet concordances will not be written into UPP",
		Name:             "Check connectivity to concordance reader/writer",
		PanicGuide:       "https://dewey.ft.com/smartlogic-concordance-transform.html",
		Severity:         3,
		TechnicalSummary: `Check health of concordance-rw-dynamodb`,
		Checker:          h.checkConcordanceRwConnectivity,
	}
}

func (h *SmartlogicConcordanceTransformerHandler) checkConcordanceRwConnectivity() error {
	urlToCheck := h.transformer.writerAddress + "__gtg"
	request, err := http.NewRequest("GET", urlToCheck, nil)
	if err != nil {
		return errors.New("Failed to create request: " + urlToCheck)
	}
	resp, err := h.transformer.httpClient.Do(request)
	if err != nil {
		return errors.New("Error " + err.Error() + " calling writer at " + urlToCheck)
	}
	resp.Body.Close()
	if resp != nil && resp.StatusCode != http.StatusOK {
		return errors.New("Writer " + urlToCheck + " returned status " + strconv.Itoa(resp.StatusCode))
	}
	return nil
}

func (h *SmartlogicConcordanceTransformerHandler) checkKafkaConnectivity() error {
	err := h.consumer.ConnectivityCheck()
	if err != nil {
		return err
	} else {
		return nil
	}
}
