package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	gidTokenRE   = regexp.MustCompile(`/g/(\d+)/([a-f0-9]+)/`)
	nextCursorRE = regexp.MustCompile(`[?&]next=([^&"\s]+)`)
	ratingRE     = regexp.MustCompile(`([0-5](?:\.\d+)?)`)
	totalCountRE = regexp.MustCompile(`Found\s+(?:about\s+)?([\d,]+)\s+results`)
	tagClassRE   = regexp.MustCompile(`^gt`)
	bgPosRE      = regexp.MustCompile(`background-position\s*:\s*(-?\d+)px\s+(-?\d+)px`)
	spaceRE      = regexp.MustCompile(`\s+`)
	thumbURLRE   = regexp.MustCompile(`url\((.+?)\)`)
	postedIDRE   = regexp.MustCompile(`^posted_`)
	digitRE      = regexp.MustCompile(`(\d+)`)
)

type GalleryListItem struct {
	GID         int64
	Token       string
	Title       string
	RatingSig   string
	RatingEst   *float64
	VisibleTags []string
	FavoritedAt *string
	IsDeleted   bool
}

type ListResult struct {
	Items      []GalleryListItem
	NextCursor *string
	TotalCount *int
}

type GalleryDetail struct {
	Title        string
	TitleJPN     string
	Category     string
	Uploader     string
	Rating       *float64
	Thumb        string
	Posted       string
	Language     string
	Pages        int
	FavCount     int
	CommentCount int
	Tags         map[string][]string
}

func normalizeText(s string) string {
	return strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
}

func extractRatingSignal(sel *goquery.Selection) (string, *float64) {
	// Try CSS sprite-based rating
	sel.Find(".ir").Each(func(_ int, ir *goquery.Selection) {
		// Already found? skip via closure
	})

	var sig string
	var est *float64

	sel.Find("[class*='ir']").EachWithBreak(func(_ int, ir *goquery.Selection) bool {
		style, _ := ir.Attr("style")
		style = normalizeText(style)
		m := bgPosRE.FindStringSubmatch(style)
		if m == nil {
			title, _ := ir.Attr("title")
			title = normalizeText(title)
			if title == "" {
				return true // continue
			}
			mt := ratingRE.FindStringSubmatch(title)
			if mt != nil {
				v, _ := strconv.ParseFloat(mt[1], 64)
				sig = "title:" + mt[1]
				est = &v
				return false // break
			}
			return true
		}
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		if y == -1 {
			v := 5.0 - float64(abs(x))/16.0
			v = clamp(v, 0, 5)
			sig = "sprite:x=" + m[1] + ",y=" + m[2]
			est = &v
			return false
		}
		if y == -21 {
			v := 4.5 - float64(abs(x))/16.0
			v = clamp(v, 0, 5)
			sig = "sprite:x=" + m[1] + ",y=" + m[2]
			est = &v
			return false
		}
		return true
	})

	if est != nil {
		return sig, est
	}

	// Fallback: text-based rating
	for _, class := range []string{".gl4e", ".gl4t", ".gl5t", ".gl5m", ".gl5c"} {
		node := sel.Find(class)
		if node.Length() == 0 {
			continue
		}
		text := normalizeText(node.Text())
		m := ratingRE.FindStringSubmatch(text)
		if m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			sig = "text:" + m[1]
			est = &v
			return sig, est
		}
	}
	return "", nil
}

func extractVisibleTags(sel *goquery.Selection) []string {
	tags := make(map[string]struct{})

	sel.Find("*").Each(func(_ int, node *goquery.Selection) {
		classes, _ := node.Attr("class")
		if classes == "" {
			return
		}
		for _, c := range strings.Fields(classes) {
			if tagClassRE.MatchString(c) {
				text := strings.ToLower(normalizeText(node.Text()))
				if text != "" && len(text) <= 80 {
					tags[text] = struct{}{}
				}
				return
			}
		}
	})

	if len(tags) > 0 {
		return mapKeys(tags)
	}

	// Fallback: f_search links
	sel.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if !strings.Contains(href, "f_search=") {
			return
		}
		text := strings.ToLower(normalizeText(a.Text()))
		if text == "" || len(text) > 80 {
			return
		}
		if text == "archive download" || text == "torrent download" {
			return
		}
		tags[text] = struct{}{}
	})
	return mapKeys(tags)
}

func ParseGalleryList(html string) (*ListResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}

	result := &ListResult{}

	// Total count
	if m := totalCountRE.FindStringSubmatch(html); m != nil {
		n, _ := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		result.TotalCount = &n
	}

	itg := doc.Find(".itg")
	if itg.Length() == 0 {
		return result, nil
	}

	// In extended mode (.itg is a <table>), iterate over <tr> rows.
	// In other modes (.itg is a <div>), iterate over direct children.
	var rows *goquery.Selection
	if goquery.NodeName(itg) == "table" {
		rows = itg.Find("tr")
	} else {
		rows = itg.Children()
	}

	rows.Each(func(_ int, el *goquery.Selection) {
		glname := el.Find(".glname")
		if glname.Length() == 0 {
			return
		}

		// Find <a> link
		a := glname.Find("a").First()
		if a.Length() == 0 {
			// Check parent
			parent := glname.Parent()
			if goquery.NodeName(parent) == "a" {
				a = parent
			}
		}
		if a.Length() == 0 {
			return
		}

		href, _ := a.Attr("href")
		m := gidTokenRE.FindStringSubmatch(href)
		if m == nil {
			return
		}
		gid, _ := strconv.ParseInt(m[1], 10, 64)
		token := m[2]

		// Title: deepest child text
		title := extractDeepestText(glname)
		ratingSig, ratingEst := extractRatingSignal(el)
		visibleTags := extractVisibleTags(el)

		// Favorited at
		var favAt *string
		el.Find("p").EachWithBreak(func(_ int, p *goquery.Selection) bool {
			if strings.TrimSpace(p.Text()) == "Favorited:" {
				next := p.Next()
				if next.Length() > 0 {
					text := strings.TrimSpace(next.Text())
					if text != "" {
						favAt = &text
					}
				}
				return false
			}
			return true
		})

		// Deleted detection
		isDeleted := false
		el.Find("[id]").Each(func(_ int, node *goquery.Selection) {
			id, _ := node.Attr("id")
			if postedIDRE.MatchString(id) && node.Find("s").Length() > 0 {
				isDeleted = true
			}
		})

		result.Items = append(result.Items, GalleryListItem{
			GID:         gid,
			Token:       token,
			Title:       title,
			RatingSig:   ratingSig,
			RatingEst:   ratingEst,
			VisibleTags: visibleTags,
			FavoritedAt: favAt,
			IsDeleted:   isDeleted,
		})
	})

	// Next cursor
	dnext := doc.Find("#dnext")
	if dnext.Length() > 0 {
		href, _ := dnext.Attr("href")
		m := nextCursorRE.FindStringSubmatch(href)
		if m != nil {
			result.NextCursor = &m[1]
		}
	}

	return result, nil
}

func ParseDetail(html string) (*GalleryDetail, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}

	gm := doc.Find(".gm")
	if gm.Length() == 0 {
		return nil, nil
	}

	d := &GalleryDetail{}

	d.Title = strings.TrimSpace(gm.Find("#gn").Text())
	d.TitleJPN = strings.TrimSpace(gm.Find("#gj").Text())

	// Category
	ce := gm.Find(".cn")
	if ce.Length() == 0 {
		ce = gm.Find(".cs")
	}
	d.Category = strings.TrimSpace(ce.Text())

	// Uploader
	d.Uploader = strings.TrimSpace(gm.Find("#gdn").Text())

	// Rating
	ratingLabel := gm.Find("#rating_label")
	if ratingLabel.Length() > 0 {
		rtext := strings.TrimSpace(ratingLabel.Text())
		if !strings.Contains(rtext, "Not Yet Rated") {
			idx := strings.Index(rtext, " ")
			if idx != -1 {
				if v, err := strconv.ParseFloat(rtext[idx+1:], 64); err == nil {
					d.Rating = &v
				}
			}
		}
	}

	// Thumb URL
	gd1 := gm.Find("#gd1 div")
	if gd1.Length() > 0 {
		style, _ := gd1.Attr("style")
		if m := thumbURLRE.FindStringSubmatch(style); m != nil {
			d.Thumb = strings.Trim(m[1], "'\"")
		}
	}

	// Detail table #gdd
	gm.Find("#gdd tr").Each(func(_ int, tr *goquery.Selection) {
		tds := tr.Find("td")
		if tds.Length() < 2 {
			return
		}
		key := strings.TrimSpace(tds.First().Text())
		value := strings.TrimSpace(tds.Last().Text())

		switch {
		case strings.HasPrefix(key, "Posted"):
			d.Posted = value
		case strings.HasPrefix(key, "Language"):
			d.Language = value
		case strings.HasPrefix(key, "Length"):
			idx := strings.Index(value, " ")
			if idx >= 0 {
				if n, err := strconv.Atoi(strings.ReplaceAll(value[:idx], ",", "")); err == nil {
					d.Pages = n
				}
			}
		case strings.HasPrefix(key, "Favorited"):
			switch {
			case value == "Never":
				d.FavCount = 0
			case value == "Once":
				d.FavCount = 1
			default:
				idx := strings.Index(value, " ")
				if idx >= 0 {
					if n, err := strconv.Atoi(strings.ReplaceAll(value[:idx], ",", "")); err == nil {
						d.FavCount = n
					}
				}
			}
		}
	})

	// Comment count
	cdiv := doc.Find("#cdiv")
	if cdiv.Length() > 0 {
		aall := cdiv.Find("#aall")
		if aall.Length() > 0 {
			if m := digitRE.FindStringSubmatch(aall.Text()); m != nil {
				d.CommentCount, _ = strconv.Atoi(m[1])
			} else {
				d.CommentCount = cdiv.Find(".c1").Length()
			}
		} else {
			d.CommentCount = cdiv.Find(".c1").Length()
		}
	}

	// Tags
	taglist := doc.Find("#taglist")
	if taglist.Length() > 0 {
		d.Tags = make(map[string][]string)
		taglist.Find("tr").Each(func(_ int, tr *goquery.Selection) {
			tds := tr.Find("td")
			if tds.Length() < 2 {
				return
			}
			ns := strings.TrimRight(strings.TrimSpace(tds.First().Text()), ":")
			if ns == "" {
				ns = "misc"
			}
			var tagValues []string
			tds.Last().Find("div a").Each(func(_ int, a *goquery.Selection) {
				t := strings.TrimSpace(a.Text())
				if t != "" {
					tagValues = append(tagValues, t)
				}
			})
			if len(tagValues) > 0 {
				d.Tags[ns] = tagValues
			}
		})
	}

	return d, nil
}

func extractDeepestText(sel *goquery.Selection) string {
	node := sel
	for {
		children := node.Children()
		if children.Length() == 0 {
			break
		}
		node = children.First()
	}
	return normalizeText(node.Text())
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
