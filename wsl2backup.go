// package main provides the binary wsl2backup.
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

var (
	distro  = flag.String("distro", "kali-linux", "The WSL distribution to backup.")
	outfile = flag.String("o", "", "Output filename, if not supplied it will be created using todays date, the distrbution name and the output type.")
	outfmt  = flag.String("f", "vhdx", "Export output type. Valid are \"tar\" and \"vhdx\" (default)")
	outzip  = flag.Bool("z", false, "Compress final output file using ZIP (default off).")
	term    = flag.Bool("t", false, "Terminate the distribution if it is running in order to back it up.")
	compact = flag.Bool("c", false, "Use Windows compact to compress the file output, this uses the built in NTFS compression instead of needing to unzip the file.")
	keep    = flag.Bool("keep", false, "Keep the uncompressed file after compression. Only valid with the -z flag.")

	// WSL Commands.
	wsl     = "wsl"
	wslList = "-l -v"

	// Compact commands.
	compactexe = "compact"
)

// wslCmd runs a WSL command with arguments "flags" and returns a slice of bytes containing
// the stdout output in UTF8 encoding.
func wslCmd(flags string) ([]byte, error) {
	// TODO: Fix to properly process quoted arguments later.
	args := strings.Split(flags, " ")
	cmd := exec.Command(wsl, args...)

	// TODO: Also capture stderr.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	result, err := io.ReadAll(stdout)
	if err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return result, err
	}

	// Return UTF8 encoded from Windows UTF16.
	win16 := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	utf16bom := unicode.BOMOverride(win16.NewDecoder())
	ur := transform.NewReader(bytes.NewReader(result), utf16bom)
	return io.ReadAll(ur)
}

// distroCheck returns true of distro is in the WSL distribution list, false if not or an error.
func distroCheck(distro string) (bool, error) {
	res, err := wslCmd(wslList)
	if err != nil {
		return false, err
	}

	type dinfo struct{ name, state, version string }
	distros := strings.Split(string(res), "\r\n")
	for i, d := range distros {
		if i == 0 {
			// Skip header.
			continue
		}

		d = strings.Replace(d, "* ", "", -1)
		fields := strings.Fields(d)

		nfo := &dinfo{}
		if len(fields) == 3 {
			nfo = &dinfo{fields[0], fields[1], fields[2]}
		}

		// WSL command is not fussy about distro case, so we don't need to be either.
		if strings.EqualFold(nfo.name, distro) {
			if nfo.state == "Stopped" {
				return true, nil
			}

			if *term {
				log.Printf("Found %v distro but it is running, terminating it as requested...\n", nfo.name)
				_, err = wslCmd(fmt.Sprintf("--terminate %s", nfo.name))
				if err != nil {
					return false, err
				}

				// Check again now, recursively.
				return distroCheck(distro)
			}

			return false, fmt.Errorf("found distribution %s but it is running and -t flag not specified so it will not be terminated", nfo.name)
		}
	}

	return false, nil
}

func wslExport(distro, format, of string) error {
	var fmtarg string
	if format == "vhdx" {
		fmtarg = " --vhd"
	}

	cmd := fmt.Sprintf("--export %s%s %s", distro, fmtarg, of)
	log.Printf("Exporting distribution %q for backup to file %q in %v format...\n", distro, of, format)
	res, err := wslCmd(cmd)
	if err != nil {
		log.Printf("Failed: %s\n", res)
		return err
	}

	log.Printf("Export suceeded: %s", res)

	return nil
}

func zipFile(fn string) error {
	zof := fn + ".zip"
	log.Printf("Compressing %s file to %s...\n", fn, zof)

	// Create the ZIP file.
	zf, err := os.Create(zof)
	if err != nil {
		return fmt.Errorf("error creating zip file: %v", err)
	}

	w := zip.NewWriter(zf)

	// Create the inner compressed file.
	cf, err := w.Create(fn)
	if err != nil {
		return fmt.Errorf("error creating zip directory: %v", err)
	}

	// Open the original file.
	uf, err := os.Open(fn)
	if err != nil {
		return fmt.Errorf("error opening exported file: %v", err)
	}

	io.Copy(cf, uf)
	if err = w.Close(); err != nil {
		return err
	}

	if err = uf.Close(); err != nil {
		return err
	}

	log.Println("Compression completed successfully.")

	return nil
}

// compactFile compresses a file using the OS compact command which uses NTFS compression to reduce the size
// of the backup on disk.
func compactFile(fn string) error {
	cmd := exec.Command(compactexe, "/c", fn)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	result, err := io.ReadAll(stdout)
	if err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}

	if strings.Contains(string(result), "1 files within 1 directories were compressed") {
		log.Println("Compression completed successfully.")
		return nil
	}

	return fmt.Errorf("compact failed: %s", result)
}

// outputName takes an output format and returns a output filename when one was not provided on
// command line.
func outputName(format, distro string) string {
	return fmt.Sprintf("%s-%s.%s", time.Now().Format("200601021504"), distro, format)
}

func main() {
	flag.Parse()

	// Validate outfmt format.
	switch *outfmt {
	case "vhdx", "tar":
	case "zip":
		log.Fatal("To output in zip format, use --z flag. --f flag is to provide the export file format (vhdx or tar).")
	default:
		log.Fatalf("Output format %q not supported. Supported formats are \"vhdx\" (default) and \"tar\".", *outfmt)
	}

	// Validate compression choice.
	if *outzip && *compact {
		log.Fatalf("Invalid arguments: Choose --z for ZIP or --c for Compact, but not both.")
	}

	// Validate distribution specified.
	d, err := distroCheck(*distro)
	if err != nil {
		log.Fatal(err)
	}

	if !d {
		log.Fatalf("Distro %q not found in WSL, check installed distribution with \"%s %s\"", *distro, wsl, wslList)
	}

	// If no output filename provided, create a sane one.
	of := *outfile
	if *outfile == "" {
		of = outputName(*outfmt, *distro)
	}

	// Do the export.
	if err = wslExport(*distro, *outfmt, of); err != nil {
		log.Fatal(err)
	}

	// ZIP the output if requested.
	if *outzip {
		if err := zipFile(of); err != nil {
			log.Fatal(err)
		}

		if !*keep {
			// Delete the original file.
			os.Remove(of)
		}

		os.Exit(0)
	}

	if *compact {
		if err := compactFile(of); err != nil {
			log.Fatalf("Error compacting file: %v", err)
		}
	}
}
