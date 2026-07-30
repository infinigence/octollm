package image_url_fetch

import (
	"errors"
	"testing"
)

func TestImageErrorsBelongToImageURLFetch(t *testing.T) {
	categories := []error{
		ErrImageRequestBody,
		ErrImageDownload,
		ErrImageResponseRead,
		ErrImageInternal,
	}

	for _, category := range categories {
		if !errors.Is(category, ErrImageURLFetch) {
			t.Fatalf("%v does not wrap ErrImageURLFetch", category)
		}
	}
}

func TestImageErrorCategoriesAreIndependent(t *testing.T) {
	categories := []error{
		ErrImageRequestBody,
		ErrImageDownload,
		ErrImageResponseRead,
		ErrImageInternal,
	}

	for i, category := range categories {
		for j, other := range categories {
			if i != j && errors.Is(category, other) {
				t.Fatalf("%v unexpectedly matches %v", category, other)
			}
		}
	}
}
