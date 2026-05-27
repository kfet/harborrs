package store

import (
	"reflect"
	"testing"
)

func TestOPMLClone(t *testing.T) {
	o := &OPML{
		Title: "T",
		Feeds: []Feed{
			{Title: "a", XMLURL: "u1", Tags: []string{"x", "y"}},
			{Title: "b", XMLURL: "u2"},
		},
	}
	c := o.Clone()
	if !reflect.DeepEqual(c, o) {
		t.Fatalf("clone not equal: %+v vs %+v", c, o)
	}
	// Mutate clone — original must be untouched.
	c.Feeds[0].Title = "MUTATED"
	c.Feeds[0].Tags[0] = "MUTATED"
	c.Feeds = append(c.Feeds, Feed{XMLURL: "u3"})
	if o.Feeds[0].Title == "MUTATED" {
		t.Fatal("clone aliased Feeds")
	}
	if o.Feeds[0].Tags[0] == "MUTATED" {
		t.Fatal("clone aliased Tags slice")
	}
	if len(o.Feeds) != 2 {
		t.Fatalf("clone aliased Feeds-len: %d", len(o.Feeds))
	}
}

func TestOPMLCloneNil(t *testing.T) {
	var o *OPML
	c := o.Clone()
	if c == nil || len(c.Feeds) != 0 {
		t.Fatalf("nil clone: %+v", c)
	}
}

func TestOPMLCloneEmptyFeeds(t *testing.T) {
	o := &OPML{Title: "T"}
	c := o.Clone()
	if c.Title != "T" || len(c.Feeds) != 0 {
		t.Fatalf("empty clone: %+v", c)
	}
}
