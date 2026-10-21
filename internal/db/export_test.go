package db

// MigrateFS exposes migrate to the tests in package db_test. Those can't be
// internal tests because they use dbtest, which imports this package.
var MigrateFS = migrate
