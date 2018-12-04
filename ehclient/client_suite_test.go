package ehclient_test

import (
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestClient(t *testing.T) {
	if os.Getenv("EVENTHUB_CONNECTION_STRING") == "" {
		//GinkgoWriter.Write("EVENTHUB_CONNECTION_STRING not defined, skipping...")
		t.Skip()
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Client Suite")
}
