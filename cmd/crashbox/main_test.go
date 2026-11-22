package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	for _, args := range [][]string{
		{"-n", "shop", "api", "--sink", "/x"},
		{"api", "-n", "shop", "--sink", "/x"},
		{"--sink", "/x", "api", "-n", "shop"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		ns := fs.String("n", "default", "")
		sink := fs.String("sink", "", "")
		pos := parseInterspersed(fs, args)
		if !reflect.DeepEqual(pos, []string{"api"}) || *ns != "shop" || *sink != "/x" {
			t.Errorf("%v: pos=%v ns=%s sink=%s", args, pos, *ns, *sink)
		}
	}
}
