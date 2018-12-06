package common

type Client interface {
	PostData(*[]byte, string) error
	PostBatchData(*[]interface{}, string) (int, error)
}
