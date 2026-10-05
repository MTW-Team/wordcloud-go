// Command example renders a JSON object of word frequencies to PNG.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	wordcloud "github.com/MTW-Team/wordcloud-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	fontPath := flag.String("font", "", "TTF/OTF font path (default: embedded Go Regular)")
	input := flag.String("input", "", "word-frequency JSON file (default: built-in sample)")
	output := flag.String("output", "wordcloud.png", "output PNG path")
	seed := flag.Uint64("seed", 42, "deterministic random seed")
	flag.Parse()
	frequencies := map[string]int{
		"Go": 100, "concurrency": 70, "performance": 60, "cloud": 50,
		"pixels": 40, "fonts": 35, "layout": 30, "color": 25,
		"render": 20, "image": 18, "words": 15, "code": 12,
	}
	if *input != "" {
		raw, err := os.ReadFile(*input)
		if err != nil {
			return err
		}
		frequencies = make(map[string]int)
		if err := json.Unmarshal(raw, &frequencies); err != nil {
			return err
		}
	}
	data := goregular.TTF
	if *fontPath != "" {
		var err error
		data, err = os.ReadFile(*fontPath)
		if err != nil {
			return err
		}
	}
	options := wordcloud.DefaultOptions()
	options.Seed = *seed
	generator, err := wordcloud.New(data, options)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	start := time.Now()
	result, err := generator.Generate(ctx, frequencies)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	f, err := os.Create(*output)
	if err != nil {
		return err
	}
	if err := result.WritePNG(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("%d/%d words, layout %s, PNG %s, %s\n", len(result.Words), len(frequencies), elapsed, time.Since(start)-elapsed, *output)
	return nil
}
