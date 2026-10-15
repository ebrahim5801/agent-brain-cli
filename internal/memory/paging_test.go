package memory

import (
	"reflect"
	"testing"
)

func TestPageItemsSlicesFilteredSequenceAndFormatsRanges(t *testing.T) {
	// These items represent a filtered list in its existing order. Paging must
	// preserve that sequence rather than reordering or re-filtering it.
	items := []int{10, 30, 50, 70, 90}
	tests := []struct {
		name       string
		page       Page
		wantItems  []int
		wantFrom   int
		wantTo     int
		wantMore   bool
		wantFooter string
	}{
		{name: "first", page: Page{Limit: 2}, wantItems: []int{10, 30}, wantFrom: 1, wantTo: 2, wantMore: true, wantFooter: "Showing 1-2 of 5."},
		{name: "middle", page: Page{Limit: 2, Offset: 2}, wantItems: []int{50, 70}, wantFrom: 3, wantTo: 4, wantMore: true, wantFooter: "Showing 3-4 of 5."},
		{name: "last", page: Page{Limit: 2, Offset: 4}, wantItems: []int{90}, wantFrom: 5, wantTo: 5, wantFooter: "Showing 5-5 of 5."},
		{name: "unlimited", page: Page{Limit: 0}, wantItems: items, wantFrom: 1, wantTo: 5},
		{name: "all fit", page: Page{Limit: 5}, wantItems: items, wantFrom: 1, wantTo: 5},
		{name: "past end", page: Page{Limit: 2, Offset: 8}, wantItems: nil, wantFrom: 0, wantTo: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PageItems(items, tt.page)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Items, tt.wantItems) {
				t.Errorf("items = %v, want %v", got.Items, tt.wantItems)
			}
			if got.Total != 5 || got.From != tt.wantFrom || got.To != tt.wantTo || got.HasMore != tt.wantMore {
				t.Errorf("page = %+v", got)
			}
			if footer := got.RangeFooter(); footer != tt.wantFooter {
				t.Errorf("footer = %q, want %q", footer, tt.wantFooter)
			}
		})
	}
}

func TestPageItemsRejectsNegativeInputs(t *testing.T) {
	for _, page := range []Page{{Limit: -1}, {Limit: 2, Offset: -1}} {
		if _, err := PageItems([]int{1}, page); err == nil {
			t.Errorf("PageItems(%+v) accepted negative input", page)
		}
	}
}
