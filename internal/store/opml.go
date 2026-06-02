package store

import (
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kfet/harb/internal/atomic"
)

// OPML is the parsed shape of a subscriptions.opml file. Only the fields
// harborrs cares about are retained on parse; on write we emit a
// minimal-but-conformant document.
type OPML struct {
	Title string
	// Feeds is the flat list. Each feed has zero or more Tags.
	Feeds []Feed
}

// Feed is a single subscription. Tags is the canonical organisation
// dimension — many-to-many, flat, GReader-compatible (each tag round-
// trips as a `user/-/label/<name>` stream).
type Feed struct {
	Title   string
	XMLURL  string
	HTMLURL string
	Tags    []string
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
	Category string       `xml:"category,attr,omitempty"`
	Outlines []xmlOutline `xml:"outline"`
}

// ParseOPML parses an OPML document from bytes. Tags on each feed are
// the union of (a) the slash-joined nested-outline parent path and
// (b) the OPML 2.0 `category` attribute (comma-separated). Result is
// deduped + sorted for stable output.
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
				tags := []string{}
				if folder != "" {
					tags = append(tags, folder)
				}
				for _, c := range strings.Split(ol.Category, ",") {
					if t := strings.TrimSpace(c); t != "" {
						tags = append(tags, t)
					}
				}
				o.Feeds = append(o.Feeds, Feed{
					Title:   title,
					XMLURL:  ol.XMLURL,
					HTMLURL: ol.HTMLURL,
					Tags:    NormalizeTags(tags),
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

// ReservedTagUntagged is the sentinel name used by the UI for the
// "no tags" pseudo-bucket in the home sidebar (`?tag=__untagged__`).
// It MUST NOT appear as a real tag — every user/client entry point
// either silently drops it (NormalizeTags / Feed.AddTag) or rejects
// the request with a 4xx (rename-tag dest).
const ReservedTagUntagged = "__untagged__"

// IsReservedTag reports whether name is one of the sentinel pseudo-
// tag names that may never round-trip as a real Feed.Tag.
func IsReservedTag(name string) bool {
	return name == ReservedTagUntagged
}

// NormalizeTags trims, dedupes and sorts a tag list. Empty input returns
// nil so feeds with no tags serialise without an empty `category` attr.
// Reserved pseudo-tag names (see IsReservedTag) are dropped.
func NormalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || IsReservedTag(t) {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// ReadOPML reads + parses an OPML file from disk.
func ReadOPML(path string) (*OPML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseOPML(data)
}

// Marshal serialises the OPML to bytes. v1 emits feeds flat at the
// body root with a comma-joined `category` attribute per OPML 2.0 —
// dropping the legacy nested-folder layout. Order is stable: feeds
// sorted by title, then URL.
func (o *OPML) Marshal() ([]byte, error) {
	feeds := append([]Feed(nil), o.Feeds...)
	sort.Slice(feeds, func(i, j int) bool {
		if feeds[i].Title != feeds[j].Title {
			return feeds[i].Title < feeds[j].Title
		}
		return feeds[i].XMLURL < feeds[j].XMLURL
	})
	outs := make([]xmlOutline, 0, len(feeds))
	for _, f := range feeds {
		outs = append(outs, xmlOutline{
			Text:     f.Title,
			Title:    f.Title,
			Type:     "rss",
			XMLURL:   f.XMLURL,
			HTMLURL:  f.HTMLURL,
			Category: strings.Join(NormalizeTags(f.Tags), ","),
		})
	}
	doc := xmlOPML{
		Version: "2.0",
		Head:    xmlHead{Title: o.Title},
		Body:    xmlBody{Outlines: outs},
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
	f.Tags = NormalizeTags(f.Tags)
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

// AllTags returns the deduped + sorted union of every feed's Tags. Used
// by the Reader tag/list endpoint and the UI sidebar.
func (o *OPML) AllTags() []string {
	seen := map[string]struct{}{}
	for _, f := range o.Feeds {
		for _, t := range f.Tags {
			seen[t] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// RenameTag rewrites every occurrence of old → new in the feed list,
// in place. Returns the number of feeds touched. If new is empty the
// rename is a no-op (use Remove-style semantics via DisableTag instead).
func (o *OPML) RenameTag(oldName, newName string) int {
	if oldName == "" || newName == "" || oldName == newName {
		return 0
	}
	n := 0
	for i := range o.Feeds {
		tags := o.Feeds[i].Tags
		changed := false
		for j, t := range tags {
			if t == oldName {
				tags[j] = newName
				changed = true
			}
		}
		if changed {
			o.Feeds[i].Tags = NormalizeTags(tags)
			n++
		}
	}
	return n
}

// DisableTag removes every occurrence of the given tag across feeds.
// Returns the number of feeds touched.
func (o *OPML) DisableTag(name string) int {
	if name == "" {
		return 0
	}
	n := 0
	for i := range o.Feeds {
		tags := o.Feeds[i].Tags
		out := tags[:0]
		for _, t := range tags {
			if t != name {
				out = append(out, t)
			}
		}
		if len(out) != len(tags) {
			if len(out) == 0 {
				out = nil
			}
			o.Feeds[i].Tags = out
			n++
		}
	}
	return n
}

// HasTag reports whether the feed carries the given tag.
func (f *Feed) HasTag(name string) bool {
	for _, t := range f.Tags {
		if t == name {
			return true
		}
	}
	return false
}

// AddTag adds the tag if not already present and re-normalises.
// Reserved pseudo-tag names (see IsReservedTag) are silently dropped.
func (f *Feed) AddTag(name string) {
	name = strings.TrimSpace(name)
	if name == "" || IsReservedTag(name) || f.HasTag(name) {
		return
	}
	f.Tags = NormalizeTags(append(f.Tags, name))
}

// RemoveTag drops the tag if present.
func (f *Feed) RemoveTag(name string) {
	if name == "" {
		return
	}
	out := f.Tags[:0]
	for _, t := range f.Tags {
		if t != name {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		f.Tags = nil
	} else {
		f.Tags = out
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
