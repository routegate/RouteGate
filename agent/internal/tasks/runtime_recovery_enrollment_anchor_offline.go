package tasks

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"syscall"
)

// An OFFLINE, independent local continuity anchor in the existing runtime
// lock directory. It exists only to catch one-sided loss of the journal's
// enrollment genesis. A privileged local attacker who destroys BOTH dirs
// can still forge a plausible fresh installation. The Manager must anchor
// actual enrollment independently before production use.
const recoveryEnrollmentAnchorFilenameV1 = "enrollment-continuity-anchor-v1.json"

func openRecoveryEnrollmentAnchorRootV1(path string) (*os.File,error) {
	fd,err:=syscall.Open(path,syscall.O_RDONLY|syscall.O_DIRECTORY|
		syscall.O_NOFOLLOW|syscall.O_CLOEXEC,0)
	if err!=nil{return nil,errRecoveryJournalUntrustedV1}
	file:=os.NewFile(uintptr(fd),path)
	info,err:=file.Stat()
	if err!=nil{_ = file.Close();return nil,errRecoveryJournalUntrustedV1}
	stat,ok:=info.Sys().(*syscall.Stat_t)
	if !ok||!info.IsDir()||info.Mode().Perm()!=0700||
		stat.Uid!=uint32(os.Geteuid()){
		_ = file.Close()
		return nil,errRecoveryJournalUntrustedV1
	}
	return file,nil
}

// Requires the real runtime.lock to be held by the caller. A successful
// write is never authorization to enroll or to run a VPN mutation.
func writeRecoveryEnrollmentAnchorUnderRuntimeLockV1(
	runtimeDir *os.File,raw []byte,step func(string)error,
)error{
	if len(raw)==0||len(raw)>recoveryJournalDiskRecordLimitV1{
		return errRecoveryJournalUntrustedV1
	}
	fd,err:=syscall.Openat(int(runtimeDir.Fd()),recoveryEnrollmentAnchorFilenameV1,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|
			syscall.O_NOFOLLOW|syscall.O_CLOEXEC,0600)
	if err!=nil{return errRecoveryJournalUntrustedV1}
	file:=os.NewFile(uintptr(fd),recoveryEnrollmentAnchorFilenameV1)
	defer file.Close()
	if step!=nil&&step("after_anchor_create")!=nil{return errRecoveryJournalUntrustedV1}
	n,err:=file.Write(raw)
	if err!=nil||n!=len(raw){return errRecoveryJournalUntrustedV1}
	if step!=nil&&step("after_anchor_write")!=nil{return errRecoveryJournalUntrustedV1}
	if err:=file.Sync();err!=nil{return errRecoveryJournalUntrustedV1}
	if step!=nil&&step("after_anchor_file_fsync")!=nil{return errRecoveryJournalUntrustedV1}
	if err:=file.Close();err!=nil{return errRecoveryJournalUntrustedV1}
	if err:=runtimeDir.Sync();err!=nil{return errRecoveryJournalUntrustedV1}
	if step!=nil&&step("after_anchor_directory_fsync")!=nil{return errRecoveryJournalUntrustedV1}
	return nil
}

// Refuses a missing anchor, symlink, hardlink, unsafe ownership/permissions,
// noncanonical bytes and mismatched Manager-claimed origin. As with the
// journal, canonical JSON and SHA-256 are NOT host attestation.
func readRecoveryEnrollmentAnchorUnderRuntimeLockV1(
	runtimeDir *os.File,
) (recoveryEnrollmentGenesisV1,error) {
	deny:=func()(recoveryEnrollmentGenesisV1,error){
		return recoveryEnrollmentGenesisV1{},errRecoveryJournalUntrustedV1
	}
	fd,err:=syscall.Openat(int(runtimeDir.Fd()),recoveryEnrollmentAnchorFilenameV1,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,0)
	if err!=nil{return deny()}
	file:=os.NewFile(uintptr(fd),recoveryEnrollmentAnchorFilenameV1)
	defer file.Close()
	info,err:=file.Stat()
	if err!=nil{return deny()}
	st,ok:=info.Sys().(*syscall.Stat_t)
	if !ok||!info.Mode().IsRegular()||info.Mode().Perm()!=0600||
		st.Uid!=uint32(os.Geteuid())||st.Nlink!=1||
		info.Size()<=0||info.Size()>recoveryJournalDiskRecordLimitV1{
		return deny()
	}
	raw,err:=io.ReadAll(io.LimitReader(file,recoveryJournalDiskRecordLimitV1+1))
	if err!=nil||int64(len(raw))!=info.Size(){return deny()}
	var got recoveryEnrollmentGenesisV1
	if err:=json.Unmarshal(raw,&got);err!=nil||!validRecoveryEnrollmentGenesisV1(got){return deny()}
	canonical,err:=json.Marshal(got)
	if err!=nil||!bytes.Equal(raw,canonical){return deny()}
	return got,nil
}
