// Package importer tracks the once-only "context_imported" flag: inside data is
// file-backed under userdata/ and read by the matcher, so the only import step is
// seeding embedded files to the runtime dir on first boot.
package importer
