package mocks

import (
	"fmt"
	"sync"

	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

type MockOmsClient struct {
	postedMessages map[string]string
	mutex          sync.Mutex
}

func NewMockOmsClient() common.Client {
	return &MockOmsClient{
		postedMessages: make(map[string]string),
	}
}

func (c *MockOmsClient) PostData(msg *[]byte, logType string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.postedMessages[logType] = string(*msg)
	return nil
}

func (c *MockOmsClient) PostBatchData(batch *[]interface{}, logType string) (int, error) {
	return 0, fmt.Errorf("Not Implemented")
}

func (c *MockOmsClient) GetPostedMessages() map[string]string {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.postedMessages
}
