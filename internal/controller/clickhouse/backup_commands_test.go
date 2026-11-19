package clickhouse

import (
	"errors"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var _ = Describe("backupStatement", func() {
	var location, withCredentials s3Location

	BeforeEach(func() {
		location = s3Location{URL: "https://bucket.s3.amazonaws.com/backups/nightly"}
		withCredentials = s3Location{URL: location.URL, AccessKeyID: "key", SecretAccessKey: "secret"}
	})

	It("should back up everything except system tables by default", func() {
		stmt := backupStatement{Location: withCredentials, ID: "op-1"}

		Expect(stmt.SQL()).To(Equal(
			"BACKUP ALL EXCEPT DATABASES system ON CLUSTER 'default' " +
				"TO S3('https://bucket.s3.amazonaws.com/backups/nightly', 'key', 'secret') " +
				"SETTINGS id = 'op-1' ASYNC"))
	})

	It("should render the selected databases and tables", func() {
		stmt := backupStatement{
			Location: withCredentials,
			ID:       "op-1",
			Scope: &v1.BackupScope{
				Databases: []string{"analytics", "we`ird"},
				Tables:    []v1.BackupTable{{Database: "default", Table: "events"}},
			},
		}

		Expect(stmt.SQL()).To(HavePrefix(
			"BACKUP DATABASE `analytics`, DATABASE `we\\`ird`, TABLE `default`.`events` ON CLUSTER 'default' TO S3("))
	})

	It("should treat an empty scope as the default one", func() {
		stmt := backupStatement{Location: withCredentials, ID: "op-1", Scope: &v1.BackupScope{}}

		Expect(stmt.SQL()).To(HavePrefix("BACKUP ALL EXCEPT DATABASES system ON CLUSTER"))
	})

	It("should render the base backup of an incremental backup", func() {
		base := s3Location{URL: "https://bucket.s3.amazonaws.com/backups/full", AccessKeyID: "key", SecretAccessKey: "secret"}
		stmt := backupStatement{Location: withCredentials, Base: &base, ID: "op-1"}

		Expect(stmt.SQL()).To(ContainSubstring(
			"SETTINGS id = 'op-1', base_backup = S3('https://bucket.s3.amazonaws.com/backups/full', 'key', 'secret') ASYNC"))
	})

	It("should allow server credentials only when the server requires the permission", func() {
		stmt := backupStatement{Location: location, ID: "op-1"}
		Expect(stmt.SQL()).To(Equal(
			"BACKUP ALL EXCEPT DATABASES system ON CLUSTER 'default' " +
				"TO S3('https://bucket.s3.amazonaws.com/backups/nightly') SETTINGS id = 'op-1' ASYNC"))

		stmt.AllowServerCredentials = true
		Expect(stmt.SQL()).To(HaveSuffix("SETTINGS id = 'op-1', s3_allow_server_credentials_in_user_queries = 1 ASYNC"))

		stmt.Location = withCredentials
		Expect(stmt.SQL()).NotTo(ContainSubstring(settingAllowServerCredentials))

		stmt.Base = &location
		Expect(stmt.SQL()).To(ContainSubstring(settingAllowServerCredentials))
	})

	It("should render user settings in a stable order and keep the managed ones", func() {
		stmt := backupStatement{
			Location: withCredentials,
			ID:       "op-1",
			Settings: map[string]string{
				"id":                 "user",
				"base_backup":        "user",
				"compression_method": "zstd",
				"compression_level":  "3",
				"deduplicate_files":  "false",
			},
		}

		Expect(stmt.SQL()).To(HaveSuffix(
			"SETTINGS id = 'op-1', compression_level = 3, compression_method = 'zstd', deduplicate_files = false ASYNC"))
	})

	It("should render a restore", func() {
		stmt := backupStatement{
			Restore:  true,
			Location: withCredentials,
			ID:       "op-2",
			Settings: map[string]string{"allow_non_empty_tables": "true"},
		}

		Expect(stmt.SQL()).To(Equal(
			"RESTORE ALL EXCEPT DATABASES system ON CLUSTER 'default' " +
				"FROM S3('https://bucket.s3.amazonaws.com/backups/nightly', 'key', 'secret') " +
				"SETTINGS id = 'op-2', allow_non_empty_tables = true, use_same_s3_credentials_for_base_backup = 1 ASYNC"))
	})

	It("should escape string literals", func() {
		stmt := backupStatement{
			Location: s3Location{URL: location.URL, AccessKeyID: "k'ey", SecretAccessKey: `s\e'cret`}, //nolint:gosec // test value
			ID:       "op-1",
		}

		Expect(stmt.SQL()).To(ContainSubstring(`'k\'ey', 's\\e\'cret')`))
	})
})

var _ = Describe("backupOperationState", func() {
	DescribeTable("should classify statuses",
		func(status string, running, succeeded bool) {
			state := backupOperationState{Status: status}
			Expect(state.running()).To(Equal(running))
			Expect(state.succeeded()).To(Equal(succeeded))
		},
		Entry("creating", "CREATING_BACKUP", true, false),
		Entry("created", "BACKUP_CREATED", false, true),
		Entry("backup failed", "BACKUP_FAILED", false, false),
		Entry("backup cancelled", "BACKUP_CANCELLED", false, false),
		Entry("restoring", "RESTORING", true, false),
		Entry("restored", "RESTORED", false, true),
		Entry("restore failed", "RESTORE_FAILED", false, false),
		Entry("restore cancelled", "RESTORE_CANCELLED", false, false),
	)
})

var _ = Describe("isDuplicateOperationID", func() {
	It("should recognize a repeated operation ID", func() {
		err := fmt.Errorf("submit: %w", &clickhouse.Exception{
			Code:    codeBadArguments,
			Message: "Cannot start backup with ID 'op-1' because there is another backup with the same ID.",
		})

		Expect(isDuplicateOperationID(err)).To(BeTrue())
	})

	It("should not match other errors", func() {
		Expect(isDuplicateOperationID(&clickhouse.Exception{Code: codeBadArguments, Message: "Unknown setting"})).To(BeFalse())
		Expect(isDuplicateOperationID(&clickhouse.Exception{Code: 598, Message: "Backup already exists"})).To(BeFalse())
		Expect(isDuplicateOperationID(errors.New("connection refused"))).To(BeFalse())
	})
})

var _ = Describe("backup error helpers", func() {
	It("should drop the stack trace from the error", func() {
		message := "Code: 499. DB::Exception: The specified bucket does not exist. (S3_ERROR), Stack trace (when copying):\n\n0. DB::Exception"

		Expect(errorSummary(message)).To(Equal("Code: 499. DB::Exception: The specified bucket does not exist. (S3_ERROR)"))
	})

	It("should limit the size of the error", func() {
		message := make([]byte, 2*maxBackupMessageLength)
		for i := range message {
			message[i] = 'a'
		}

		Expect(errorSummary(string(message))).To(HaveLen(maxBackupMessageLength + len("...")))
	})

	It("should hide the storage credentials", func() {
		stmt := backupStatement{
			Location: s3Location{URL: "u", AccessKeyID: "key", SecretAccessKey: "sec'ret"},
			Base:     &s3Location{URL: "b", AccessKeyID: "key", SecretAccessKey: "base-secret"},
		}

		Expect(redactSecrets(`failed at S3('u', 'key', 'sec\'ret') and sec'ret with S3('b', 'key', 'base-secret')`, stmt)).To(Equal(
			"failed at S3('u', 'key', '[HIDDEN]') and [HIDDEN] with S3('b', 'key', '[HIDDEN]')"))
	})
})
