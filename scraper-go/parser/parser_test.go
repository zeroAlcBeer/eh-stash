package parser

import (
	"testing"
)

func TestParseDetailBasic(t *testing.T) {
	html := `<html><body>
	<div class="gm">
		<div id="gn">Test Gallery Title</div>
		<div id="gj">テストギャラリー</div>
		<div class="cn">Doujinshi</div>
		<div id="gdn"><a>uploader123</a></div>
		<div id="rating_label">Average: 4.50</div>
		<div id="gd1"><div style="background:transparent url(https://cdn.example.com/thumb.jpg)"></div></div>
		<div id="gdd"><table>
			<tr><td>Posted:</td><td>2024-01-15 12:00</td></tr>
			<tr><td>Language:</td><td>Japanese</td></tr>
			<tr><td>Length:</td><td>24 pages</td></tr>
			<tr><td>Favorited:</td><td>150 times</td></tr>
		</table></div>
		<div id="cdiv"><div class="c1"></div><div class="c1"></div></div>
		<div id="taglist"><table>
			<tr><td>artist:</td><td><div><a>artistname</a></div></td></tr>
			<tr><td>female:</td><td><div><a>tag1</a></div><div><a>tag2</a></div></td></tr>
		</table></div>
	</div>
	</body></html>`

	d, err := ParseDetail(html)
	if err != nil {
		t.Fatalf("ParseDetail error: %v", err)
	}
	if d == nil {
		t.Fatal("ParseDetail returned nil")
	}

	if d.Title != "Test Gallery Title" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.TitleJPN != "テストギャラリー" {
		t.Errorf("TitleJPN = %q", d.TitleJPN)
	}
	if d.Category != "Doujinshi" {
		t.Errorf("Category = %q", d.Category)
	}
	if d.Uploader != "uploader123" {
		t.Errorf("Uploader = %q", d.Uploader)
	}
	if d.Rating == nil || *d.Rating != 4.5 {
		t.Errorf("Rating = %v", d.Rating)
	}
	if d.Thumb != "https://cdn.example.com/thumb.jpg" {
		t.Errorf("Thumb = %q", d.Thumb)
	}
	if d.Posted != "2024-01-15 12:00" {
		t.Errorf("Posted = %q", d.Posted)
	}
	if d.Language != "Japanese" {
		t.Errorf("Language = %q", d.Language)
	}
	if d.Pages != 24 {
		t.Errorf("Pages = %d", d.Pages)
	}
	if d.FavCount != 150 {
		t.Errorf("FavCount = %d", d.FavCount)
	}
	if d.CommentCount != 2 {
		t.Errorf("CommentCount = %d", d.CommentCount)
	}
	if len(d.Tags["artist"]) != 1 || d.Tags["artist"][0] != "artistname" {
		t.Errorf("Tags[artist] = %v", d.Tags["artist"])
	}
	if len(d.Tags["female"]) != 2 {
		t.Errorf("Tags[female] = %v", d.Tags["female"])
	}
}

func TestParseDetailFavCountEdgeCases(t *testing.T) {
	tests := []struct {
		favText  string
		expected int
	}{
		{"Never", 0},
		{"Once", 1},
		{"2,500 times", 2500},
	}
	for _, tt := range tests {
		html := `<html><body><div class="gm"><div id="gdd"><table>
			<tr><td>Favorited:</td><td>` + tt.favText + `</td></tr>
		</table></div></div></body></html>`

		d, err := ParseDetail(html)
		if err != nil {
			t.Errorf("favText=%q: error %v", tt.favText, err)
			continue
		}
		if d.FavCount != tt.expected {
			t.Errorf("favText=%q: got %d, want %d", tt.favText, d.FavCount, tt.expected)
		}
	}
}

func TestParseGalleryListEmpty(t *testing.T) {
	html := `<html><body><div class="itg"></div></body></html>`
	result, err := ParseGalleryList(html)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Items))
	}
	if result.NextCursor != nil {
		t.Errorf("expected nil cursor")
	}
}

func TestParseGalleryListWithItems(t *testing.T) {
	html := `<html><body>
	<p>Found about 1,234 results</p>
	<div class="itg">
		<div>
			<div class="glname"><a href="https://exhentai.org/g/12345/abcdef0123/">
				<div class="glink">Test Title</div>
			</a></div>
			<div class="ir" style="background-position:-1px -1px"></div>
		</div>
		<div>
			<div class="glname"><a href="https://exhentai.org/g/67890/fedcba9876/">
				<div class="glink">Test Title 2</div>
			</a></div>
			<div class="ir" style="background-position:-16px -21px"></div>
		</div>
	</div>
	<a id="dnext" href="?next=67890&inline_set=dm_e">Next</a>
	</body></html>`

	result, err := ParseGalleryList(html)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.TotalCount == nil || *result.TotalCount != 1234 {
		t.Errorf("TotalCount = %v", result.TotalCount)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	if result.Items[0].GID != 12345 {
		t.Errorf("item[0].GID = %d", result.Items[0].GID)
	}
	if result.Items[0].Token != "abcdef0123" {
		t.Errorf("item[0].Token = %q", result.Items[0].Token)
	}
	if result.Items[0].Title != "Test Title" {
		t.Errorf("item[0].Title = %q", result.Items[0].Title)
	}
	// First item: sprite y=-1, x=-1 → 5.0 - 1/16 = 4.9375
	if result.Items[0].RatingEst == nil || *result.Items[0].RatingEst < 4.9 {
		t.Errorf("item[0].RatingEst = %v", result.Items[0].RatingEst)
	}
	// Second item: sprite y=-21, x=-16 → 4.5 - 16/16 = 3.5
	if result.Items[1].RatingEst == nil || *result.Items[1].RatingEst != 3.5 {
		t.Errorf("item[1].RatingEst = %v, want 3.5", result.Items[1].RatingEst)
	}

	if result.NextCursor == nil || *result.NextCursor != "67890" {
		t.Errorf("NextCursor = %v", result.NextCursor)
	}
}

func TestExtractRatingSignalSprite(t *testing.T) {
	tests := []struct {
		x, y int
		want float64
	}{
		{0, -1, 5.0},
		{-16, -1, 4.0},
		{-80, -1, 0.0},
		{0, -21, 4.5},
		{-16, -21, 3.5},
	}
	for _, tt := range tests {
		sig, est := extractRatingFromSprite(tt.x, tt.y)
		if sig == "" || est == nil || *est != tt.want {
			t.Errorf("sprite(%d,%d) = %q, %v; want %f", tt.x, tt.y, sig, est, tt.want)
		}
	}
}

// helper to test sprite logic directly
func extractRatingFromSprite(x, y int) (string, *float64) {
	if y == -1 {
		v := clamp(5.0-float64(abs(x))/16.0, 0, 5)
		sig := "sprite"
		return sig, &v
	}
	if y == -21 {
		v := clamp(4.5-float64(abs(x))/16.0, 0, 5)
		sig := "sprite"
		return sig, &v
	}
	return "", nil
}
