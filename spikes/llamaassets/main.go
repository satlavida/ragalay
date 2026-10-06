// Prints the llama.cpp CPU release assets (URL + sha256) that yzma would
// install, so ragalay can pin them without importing yzma's installer.
package main

import (
	"fmt"

	"github.com/hybridgroup/yzma/pkg/download"
)

func main() {
	fmt.Println("default version:", download.DefaultVersion)
	r := download.DefaultResolver.(download.AssetResolver)
	for _, t := range [][2]string{{"amd64", "windows"}, {"arm64", "darwin"}, {"amd64", "darwin"}, {"amd64", "linux"}, {"arm64", "linux"}} {
		arch, _ := download.ParseArch(t[0])
		os, _ := download.ParseOS(t[1])
		tag, _ := download.LlamaNightlyTag(download.DefaultTag())
		target := download.Target{Arch: arch, OS: os, Processor: download.MustParseProcessor("cpu"), Version: download.DefaultTag(), UpstreamVersion: tag}
		assets, err := r.ResolveAssets(target)
		fmt.Println(t, "upstream", tag, "err", err)
		for _, a := range assets {
			fmt.Printf("  %+v\n", a)
		}
	}
}
