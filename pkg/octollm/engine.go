package octollm

type Engine interface {
	// Process executes the request and returns the response, optionally a stream channel if the response is a event stream.
	// The Endpoint determines whether to return a stream channel based on the request.
	Process(req *Request) (*Response, error)
}
