package image_url_fetch

import (
	"errors"
	"fmt"
)

var ErrImageURLFetch = errors.New("image url fetch engine")

// ImageURLFetchEngine errors are grouped into four categories. Each category
// wraps ErrImageURLFetch, so callers can first identify errors from this engine
// and then inspect only the category they need.
var (
	ErrImageRequestBody  = fmt.Errorf("%w: request body error", ErrImageURLFetch)
	ErrImageDownload     = fmt.Errorf("%w: image download error", ErrImageURLFetch)
	ErrImageResponseRead = fmt.Errorf("%w: image response read error", ErrImageURLFetch)
	ErrImageInternal     = fmt.Errorf("%w: internal error", ErrImageURLFetch)
)
