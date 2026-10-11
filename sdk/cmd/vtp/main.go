package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/Necho-dev/vartio/sdk"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "pubkey":
		pubkey(os.Args[2:])
	case "pack":
		pack(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage:\n  vtp keygen -id ID -o plugin-signing.key\n  vtp pubkey -key plugin-signing.key\n  vtp pack -manifest manifest.json -bin vartio-plugin -key plugin-signing.key -o out.vtp\n")
	os.Exit(2)
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	id := fs.String("id", "", "key id")
	out := fs.String("o", "", "output path")
	_ = fs.Parse(args)
	if *id == "" || *out == "" {
		usage()
	}
	k, err := sdk.GenerateKey(*id)
	if err != nil {
		die(err)
	}
	if err := sdk.WriteKey(*out, k); err != nil {
		die(err)
	}
	fmt.Println(k.PublicString())
}

func pubkey(args []string) {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	path := fs.String("key", "", "key file")
	_ = fs.Parse(args)
	k, err := sdk.ReadKey(*path)
	if err != nil {
		die(err)
	}
	fmt.Printf("%s %s\n", k.ID, k.PublicString())
}

func pack(args []string) {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	manifest := fs.String("manifest", "manifest.json", "manifest path")
	bin := fs.String("bin", sdk.BinaryName, "plugin binary")
	keyPath := fs.String("key", "", "signing key")
	out := fs.String("o", "", "output .vtp")
	_ = fs.Parse(args)
	if *keyPath == "" || *out == "" {
		usage()
	}
	key, err := sdk.ReadKey(*keyPath)
	if err != nil {
		die(err)
	}
	man, err := os.ReadFile(*manifest)
	if err != nil {
		die(err)
	}
	binary, err := os.ReadFile(*bin)
	if err != nil {
		die(err)
	}
	blob, err := sdk.Pack(man, binary, key)
	if err != nil {
		die(err)
	}
	if err := os.WriteFile(*out, blob, 0o644); err != nil {
		die(err)
	}
	sum := sha256.Sum256(blob)
	fmt.Printf("%s  %d  %s\n", hex.EncodeToString(sum[:]), len(blob), *out)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
