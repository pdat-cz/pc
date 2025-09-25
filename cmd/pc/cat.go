package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pdat-cz/pc/pkg/addr"
	"github.com/pdat-cz/pc/pkg/proto"
	mb "github.com/pdat-cz/pc/pkg/proto/modbus"
)

func runCat(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: pc cat <uri> <addr> [addr...]")
	}
	uri := args[0]
	specs := make([]proto.ReadSpec, 0, len(args)-1)
	for _, a := range args[1:] {
		rs, err := addr.ParseReadSpec(a)
		if err != nil {
			return fmt.Errorf("invalid addr %q: %w", a, err)
		}
		specs = append(specs, rs)
	}
	var c proto.Client = mb.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Open(ctx, uri); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	vals, err := c.Read(ctx, specs)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, v := range vals {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return nil
}
