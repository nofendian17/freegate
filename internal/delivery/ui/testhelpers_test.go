package ui

import (
	"io/fs"
	"testing"

	"freegate/web"
)

func webTemplatesFS(_ *testing.T) fs.FS { return web.Templates() }
func webStaticFS(_ *testing.T) fs.FS    { return web.Static() }
