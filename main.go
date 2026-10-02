package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var (
	receiverType = flag.String("r", "", "Method receiver type")
	argumentType = flag.String("a", "", "Method argument type")
	inverseCopy  = flag.Bool("i", false, "Inverse copying direction: from receiver into argument")
	configPath   = flag.String("f", "", "Yaml config file")
)

func Usage() {
	fmt.Fprintf(os.Stderr, `Usage of copier:
	copier -r ReceiverType -a ArgumentTypeFullPath
	copier -f copier.yaml`)
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	flag.PrintDefaults()
}

func main() {
	flag.Usage = Usage
	flag.Parse()
	if err := run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

const (
	cfgMode byte = 0b00000001
	argMode byte = 0b00000110
)

func run() error {
	var mode byte
	if *configPath != "" {
		mode |= 1 << 0
	}
	if *receiverType != "" {
		mode |= 1 << 1
	}
	if *argumentType != "" {
		mode |= 1 << 2
	}
	var cfg *config
	var err error
	dir := "."
	switch mode {
	case cfgMode:
		if *inverseCopy {
			return fmt.Errorf("config file mode does not allow -i option")
		}
		cfg, err = configFromFile(*configPath)
		dir = filepath.Dir(*configPath)
	case argMode:
		cfg = configFromArgs(*receiverType, *argumentType, *inverseCopy)
	default:
		return fmt.Errorf("set (only) -f option or -r and -a")
	}
	if err != nil {
		return err
	}
	files, err := generate(cfg, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
