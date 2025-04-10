package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// extractBinaryFromTarGz extracts the binary from a tar.gz archive
func extractBinaryFromTarGz(gzipStream io.Reader, binaryName string) (io.Reader, error) {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}

	tarReader := tar.NewReader(uncompressedStream)

	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("tar reading error: %w", err)
		}

		// We only care about files (not directories)
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Skip paths with directories for simplicity
		filename := filepath.Base(header.Name)

		// If this is our binary (might need to adjust for Windows .exe)
		if filename == binaryName || filename == binaryName+".exe" {
			// Copy the binary to a buffer
			var buffer []byte
			buffer, err = io.ReadAll(tarReader)
			if err != nil {
				return nil, fmt.Errorf("failed to read binary from tar: %w", err)
			}

			return io.NopCloser(strings.NewReader(string(buffer))), nil
		}
	}

	return nil, fmt.Errorf("binary %s not found in archive", binaryName)
}

// extractBinaryFromZip extracts the binary from a .zip archive
func extractBinaryFromZip(zipFile io.ReaderAt, fileSize int64, binaryName string) (io.Reader, error) {
	zipReader, err := zip.NewReader(zipFile, fileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create zip reader: %w", err)
	}

	for _, file := range zipReader.File {
		// Skip directories
		if file.FileInfo().IsDir() {
			continue
		}

		// Check if this is our binary
		filename := filepath.Base(file.Name)
		if filename == binaryName || filename == binaryName+".exe" {
			fileReader, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open file in zip: %w", err)
			}

			// Copy to buffer since we need to close the fileReader
			buffer, err := io.ReadAll(fileReader)
			fileReader.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to read binary from zip: %w", err)
			}

			return io.NopCloser(strings.NewReader(string(buffer))), nil
		}
	}

	return nil, fmt.Errorf("binary %s not found in zip archive", binaryName)
}
