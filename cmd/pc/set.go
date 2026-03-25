package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pdat-cz/pc/pkg/addr"
)

func runSet(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: pc set <uri> <addr=value> [addr=value...]")
	}
	uri := args[0]
	pairs := args[1:]
	c, err := newClientFromURI(uri)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Open(ctx, uri); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	for _, p := range pairs {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid pair %q, expected addr=value", p)
		}
		rs, err := addr.ParseReadSpec(kv[0])
		if err != nil {
			return fmt.Errorf("invalid addr %q: %w", kv[0], err)
		}
		val := kv[1]
		if err := c.Write(ctx, rs, val); err != nil {
			return fmt.Errorf("write %s: %w", kv[0], err)
		}
		fmt.Println("OK", kv[0], "=", val)
	}
	return nil
}
