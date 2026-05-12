package store

import (
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kfet/harborrs/internal/atomic"
)

// OPML is the parsed shape of a subscriptions.opml file. Only the fields
// harborrs cares about are retained on parse; on write we emit a
// minimal-but-conformant document.
type OPML struct {
	Title string
	// Feeds is the flat list. Each feed has a Folder (may be empty for
	// uncategorised) — we flatten on read and re-nest on write.
	Feeds []Feed
}

// Feed is a single subscription.
type Feed struct {
	Title   string
	XMLURL  string
	HTMLURL string
	Folder  string
}

// xmlOPML is the on-disk shape.
type xmlOPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    xmlHead  `xml:"head"`
	Body    xmlBody  `xml:"body"`
}

type xmlHead struct {
	Title string `xml:"title,omitempty"`
}

type xmlBody struct {
	Outlines []xmlOutline `xml:"outline"`
}

type xmlOutline struct {
	Text     string       `xml:"text,attr,omitempty"`
	Title    string       `xml:"title,attr,omitempty"`
	Type     string       `xml:"type,attr,omitempty"`
	XMLURL   string       `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string       `xml:"htmlUrl,attr,omitempty"`
	Outlines []xmlOutline `xml:"outline"`
}

// ParseOPML parses an OPML document from bytes.
func ParseOPML(data []byte) (*OPML, error) {
	var x xmlOPML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("opml: parse: %w", err)
	}
	o := &OPML{Title: x.Head.Title}
	var walk func(folder string, outs []xmlOutline)
	walk = func(folder string, outs []xmlOutline) {
		for _, ol := range outs {
			if ol.XMLURL != "" {
				title := firstNonEmpty(ol.Title, ol.Text, ol.XMLURL)
				o.Feeds = append(o.Feeds, Feed{
					Title:   title,
					XMLURL:  ol.XMLURL,
					HTMLURL: ol.HTMLURL,
					Folder:  folder,
				})
			}
			if len(ol.Outlines) > 0 {
				name := firstNonEmpty(ol.Title, ol.Text)
				sub := folder
				if name != "" {
					if sub == "" {
						sub = name
					} else {
						sub = sub + "/" + name
					}
				}
				walk(sub, ol.Outlines)
			}
		}
	}
	walk("", x.Body.Outlines)
	return o, nil
}

// ReadOPML reads + parses an OPML file from disk.
func ReadOPML(path string) (*OPML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseOPML(data)
}

// Marshal serialises the OPML to bytes with stable, deterministic ordering
// (folders sorted by name, feeds within each folder sorted by title then URL).
func (o *OPML) Marshal() ([]byte, error) {
	// Group feeds by top-level folder. Nested "a/b" folder names become a
	// single flat folder for simplicity in v0.1; this round-trips cleanly.
	byFolder := map[string][]Feed{}
	for _, f := range o.Feeds {
		byFolder[f.Folder] = append(byFolder[f.Folder], f)
	}
	folders := make([]string, 0, len(byFolder))
	for k := range byFolder {
		folders = append(folders, k)
	}
	sort.Strings(folders)

	var top []xmlOutline
	for _, folder := range folders {
		feeds := byFolder[folder]
		sort.Slice(feeds, func(i, j int) bool {
			if feeds[i].Title != feeds[j].Title {
				return feeds[i].Title < feeds[j].Title
			}
			return feeds[i].XMLURL < feeds[j].XMLURL
		})
		outs := make([]xmlOutline, 0, len(feeds))
		for _, f := range feeds {
			outs = append(outs, xmlOutline{
				Text:    f.Title,
				Title:   f.Title,
				Type:    "rss",
				XMLURL:  f.XMLURL,
				HTMLURL: f.HTMLURL,
			})
		}
		if folder == "" {
			top = append(top, outs...)
			continue
		}
		top = append(top, xmlOutline{
			Text:     folder,
			Title:    folder,
			Outlines: outs,
		})
	}

	doc := xmlOPML{
		Version: "2.0",
		Head:    xmlHead{Title: o.Title},
		Body:    xmlBody{Outlines: top},
	}
	out, err := xmlMarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("opml: marshal: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// WriteOPML atomically writes the OPML to path.
func (o *OPML) WriteOPML(path string) error {
	data, err := o.Marshal()
	if err != nil {
		return err
	}
	return atomic.WriteFile(path, data)
}

// Add inserts or updates a feed by XML URL. Returns true if new.
func (o *OPML) Add(f Feed) bool {
	for i, existing := range o.Feeds {
		if existing.XMLURL == f.XMLURL {
			o.Feeds[i] = f
			return false
		}
	}
	o.Feeds = append(o.Feeds, f)
	return true
}

// Remove drops a feed by XML URL. Returns true if it was present.
func (o *OPML) Remove(xmlURL string) bool {
	for i, f := range o.Feeds {
		if f.XMLURL == xmlURL {
			o.Feeds = append(o.Feeds[:i], o.Feeds[i+1:]...)
			return true
		}
	}
	return false
}

// Find returns the feed with the given XML URL, or nil.
func (o *OPML) Find(xmlURL string) *Feed {
	for i := range o.Feeds {
		if o.Feeds[i].XMLURL == xmlURL {
			return &o.Feeds[i]
		}
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
