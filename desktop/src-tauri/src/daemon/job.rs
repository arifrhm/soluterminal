use std::ffi::c_void;
use std::os::windows::io::AsRawHandle;
use std::process::Child;
use std::sync::OnceLock;

// Direct Win32 FFI — avoids a heavy windows-sys / windows-crate
// dependency for the handful of functions we need. Job-object
// ABI is stable across every supported Windows version.

type Handle = *mut c_void;
type Bool = i32;

// SECURITY_ATTRIBUTES we always pass null; JobObjectInfoClass 9 is
// JobObjectExtendedLimitInformation. 0x2000 is
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (the critical bit: "terminate
// every process in the job when the last handle to the job is
// closed"). Values per MSDN winnt.h / winbase.h.
const JOBOBJECT_EXTENDED_LIMIT_INFORMATION: i32 = 9;
const JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: u32 = 0x2000;
// 0x0800 = JOB_OBJECT_LIMIT_BREAKAWAY_OK: lets a child created with
// CREATE_BREAKAWAY_FROM_JOB leave this job. The daemon (solud) spawns
// each pane agent with that flag so the agent owns its own job tree;
// without permission here, CreateProcess fails with ERROR_ACCESS_DENIED
// and no pane can open. See daemon/spawn_windows.go.
const JOB_OBJECT_LIMIT_BREAKAWAY_OK: u32 = 0x0800;

#[repr(C)]
#[derive(Default, Clone, Copy)]
struct IoCounters {
    read_ops: u64,
    write_ops: u64,
    other_ops: u64,
    read_bytes: u64,
    write_bytes: u64,
    other_bytes: u64,
}

#[repr(C)]
#[derive(Default, Clone, Copy)]
struct BasicLimit {
    per_process_user_time: i64,
    per_job_user_time: i64,
    limit_flags: u32,
    min_working_set_size: usize,
    max_working_set_size: usize,
    active_process_limit: u32,
    affinity: usize,
    priority_class: u32,
    scheduling_class: u32,
}

#[repr(C)]
#[derive(Default, Clone, Copy)]
struct ExtendedLimit {
    basic_limit: BasicLimit,
    io_info: IoCounters,
    process_memory_limit: usize,
    job_memory_limit: usize,
    peak_process_memory_used: usize,
    peak_job_memory_used: usize,
}

#[link(name = "kernel32")]
extern "system" {
    fn CreateJobObjectW(sa: *const c_void, name: *const u16) -> Handle;
    fn SetInformationJobObject(job: Handle, class: i32, info: *const c_void, len: u32) -> Bool;
    fn AssignProcessToJobObject(job: Handle, process: Handle) -> Bool;
}

/// Transparent wrapper so OnceLock can hold a raw HANDLE across
/// threads. The handle is process-local and used only through
/// thread-safe Win32 calls.
struct JobHandle(Handle);
unsafe impl Send for JobHandle {}
unsafe impl Sync for JobHandle {}

static JOB: OnceLock<JobHandle> = OnceLock::new();

fn ensure_job() -> Handle {
    JOB.get_or_init(|| unsafe {
        let job = CreateJobObjectW(std::ptr::null(), std::ptr::null());
        if job.is_null() {
            eprintln!("job: CreateJobObjectW returned null; orphan cleanup disabled");
            return JobHandle(std::ptr::null_mut());
        }
        let mut info = ExtendedLimit::default();
        info.basic_limit.limit_flags =
            JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | JOB_OBJECT_LIMIT_BREAKAWAY_OK;
        let ok = SetInformationJobObject(
            job,
            JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
            &info as *const _ as *const c_void,
            std::mem::size_of::<ExtendedLimit>() as u32,
        );
        if ok == 0 {
            eprintln!("job: SetInformationJobObject failed; orphan cleanup disabled");
        }
        JobHandle(job)
    })
    .0
}

/// Assign a freshly-spawned child to the kill-on-job-close job.
/// Best-effort: errors are logged but don't fail the spawn — a
/// missed assignment just means that particular child could leak
/// if Tauri crashes before Drop runs.
pub(super) fn assign(child: &Child) {
    let job = ensure_job();
    if job.is_null() {
        return;
    }
    let child_handle = child.as_raw_handle() as Handle;
    unsafe {
        if AssignProcessToJobObject(job, child_handle) == 0 {
            eprintln!(
                "job: AssignProcessToJobObject failed for pid {}; it will leak on quit",
                child.id()
            );
        }
    }
}
