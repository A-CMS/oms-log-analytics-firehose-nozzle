package ehclient_test

import (
	"os"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/ehclient"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("EventHubsClient", func() {
	var (
		connectionString string
		logger           lager.Logger
	)

	connectionString = os.Getenv("EVENTHUB_CONNECTION_STRING")
	logger = lager.NewLogger("test")
	logger.RegisterSink(lager.NewWriterSink(os.Stderr, lager.DEBUG))

	BeforeEach(func() {
		// Setup
	})

	It("routes a LogMessage", func() {
		c := ehclient.NewEventHubsClient(connectionString, logger)
		bytes := []byte("Hello World")
		err := c.PostData(&bytes, "TEST")
		Expect(err).To(BeNil())
	})
})
