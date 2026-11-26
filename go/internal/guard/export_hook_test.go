package guard

// ReadExportForTest is the guard's own export reader, for the writer test of package guard_test.
func ReadExportForTest(path string) error {
	_, err := readExport(path)
	return err
}
