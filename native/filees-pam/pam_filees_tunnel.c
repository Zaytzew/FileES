/*
 * pam_filees_tunnel - the Linux counterpart of OpenBSD's BSD Authentication
 * style "-filees" for the one account that needs it, _filees-tunnel.
 *
 * On OpenBSD sshd runs /usr/libexec/auth/login_-filees (filees-ssh-auth)
 * twice: once for a challenge, once with the challenge and the client's
 * answer. Linux has no BSD Authentication, so sshd reaches the same helper
 * through keyboard-interactive:pam and this module. The module holds no
 * policy of its own: it carries the helper's challenge to the client and the
 * client's answer back to the helper, speaking the helper's existing protocol
 * on descriptor 3. Whether an OTP, a pairing token or a reconnect signature is
 * acceptable is decided only there, by the same code on both systems.
 *
 * It is meant for a dedicated PAM service selected in sshd_config with
 *     Match User _filees-tunnel
 *         PAMServiceName filees-tunnel
 * so the system's own sshd PAM stack is never edited. Any other user
 * reaching this module is a misconfiguration and is refused.
 *
 * sshd calls PAM as root; the helper runs as the FileES state owner, the
 * identity the set-id bit gives it on OpenBSD.
 */
#define _GNU_SOURCE
#define PAM_SM_AUTH
#define PAM_SM_ACCOUNT
#define PAM_SM_SESSION

#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <poll.h>
#include <pwd.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <syslog.h>
#include <unistd.h>

#include <security/pam_ext.h>
#include <security/pam_modules.h>

#define TUNNEL_USER "_filees-tunnel"
#define TUNNEL_CLASS "filees-tunnel"
#define DEFAULT_HELPER "/usr/local/libexec/filees/filees-ssh-auth"
#define DEFAULT_STATE_USER "_filees-state"

/* The helper refuses more than 2048 bytes of response and 1024 per field. */
#define MAX_FIELD 1024
#define MAX_REPLY 4096
#define HELPER_TIMEOUT_MS 20000

struct settings {
    const char *helper;
    const char *state_user;
};

static struct settings parse_settings(int argc, const char **argv)
{
    struct settings s = { DEFAULT_HELPER, DEFAULT_STATE_USER };
    int i;
    for (i = 0; i < argc; i++) {
        if (!strncmp(argv[i], "helper=", 7)) s.helper = argv[i] + 7;
        else if (!strncmp(argv[i], "state_user=", 11)) s.state_user = argv[i] + 11;
    }
    return s;
}

static void wipe(char *p, size_t n)
{
    volatile char *v = (volatile char *)p;
    while (n--) *v++ = 0;
}

static int write_all(int fd, const char *buf, size_t len)
{
    while (len > 0) {
        ssize_t n = send(fd, buf, len, MSG_NOSIGNAL);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        buf += n;
        len -= (size_t)n;
    }
    return 0;
}

/* Child side: become the state owner, keep only descriptor 3, exec. */
static void exec_helper(const struct settings *s, const char *service, int channel)
{
    struct passwd *pw = getpwnam(s->state_user);
    char *const envp[] = { NULL };
    char *argv[6];
    long fd, max;

    if (pw == NULL || pw->pw_uid == 0) _exit(126);
    if (channel != 3) {
        if (dup2(channel, 3) < 0) _exit(126);
    }
    max = sysconf(_SC_OPEN_MAX);
    if (max < 0 || max > 65536) max = 65536;
    for (fd = 0; fd < max; fd++) {
        if (fd != 3) close((int)fd);
    }
    /* Descriptors 0-2 must exist, or the first file the helper opens becomes
     * its stderr. */
    if (open("/dev/null", O_RDWR) != 0 || dup(0) != 1 || dup(0) != 2) _exit(126);
    if (setgroups(0, NULL) != 0 || setgid(pw->pw_gid) != 0 || setuid(pw->pw_uid) != 0) _exit(126);
    if (getuid() == 0 || geteuid() == 0) _exit(126);
    if (chdir("/") != 0) _exit(126);
    umask(077);

    argv[0] = (char *)"login_-filees";
    argv[1] = (char *)"-s";
    argv[2] = (char *)service;
    argv[3] = (char *)TUNNEL_USER;
    argv[4] = (char *)TUNNEL_CLASS;
    argv[5] = NULL;
    execve(s->helper, argv, envp);
    _exit(127);
}

/*
 * Runs the helper for one service. request (may be NULL) is written to
 * descriptor 3 first; everything the helper writes back is returned in reply,
 * NUL-terminated. Returns 0 only if the helper could be run and answered
 * within the limits; the caller still has to read the answer.
 */
static int run_helper(pam_handle_t *pamh, const struct settings *s, const char *service,
                      const char *request, size_t request_len, char *reply, size_t reply_size)
{
    int sv[2];
    pid_t pid;
    size_t used = 0;
    int ok = 0, status = 0;

    if (socketpair(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0, sv) != 0) {
        pam_syslog(pamh, LOG_ERR, "socketpair: %m");
        return -1;
    }
    pid = fork();
    if (pid < 0) {
        pam_syslog(pamh, LOG_ERR, "fork: %m");
        close(sv[0]);
        close(sv[1]);
        return -1;
    }
    if (pid == 0) {
        exec_helper(s, service, sv[1]);
        _exit(127);
    }
    close(sv[1]);

    if (request == NULL || write_all(sv[0], request, request_len) == 0) {
        shutdown(sv[0], SHUT_WR);
        for (;;) {
            struct pollfd p = { sv[0], POLLIN, 0 };
            ssize_t n;
            int ready = poll(&p, 1, HELPER_TIMEOUT_MS);
            if (ready < 0 && errno == EINTR) continue;
            if (ready <= 0) break; /* timeout or error: not ok */
            n = read(sv[0], reply + used, reply_size - 1 - used);
            if (n < 0 && errno == EINTR) continue;
            if (n < 0) break;
            if (n == 0) { ok = 1; break; }
            used += (size_t)n;
            if (used >= reply_size - 1) break; /* oversized answer: not ok */
        }
    }
    reply[used] = '\0';
    close(sv[0]);
    if (!ok) kill(pid, SIGKILL);
    while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {}
    if (!ok) {
        pam_syslog(pamh, LOG_ERR, "FileES authentication helper did not answer (%s)", service);
        return -1;
    }
    if (WIFEXITED(status) && (WEXITSTATUS(status) == 126 || WEXITSTATUS(status) == 127)) {
        pam_syslog(pamh, LOG_ERR, "FileES authentication helper could not be started (%s)", s->helper);
        return -1;
    }
    return 0;
}

/*
 * Extracts the challenge from "value challenge <text>\n". BSD Authentication
 * decodes the two-character escape "\n" inside a value into a newline, and the
 * helper relies on that, so the same decoding is done here.
 */
static int parse_challenge(const char *reply, char *challenge, size_t size)
{
    static const char prefix[] = "value challenge ";
    const char *line = reply;
    size_t out = 0;

    while (line != NULL && *line != '\0') {
        if (!strncmp(line, prefix, sizeof prefix - 1)) {
            const char *p = line + sizeof prefix - 1;
            while (*p != '\0' && *p != '\n') {
                char c = *p++;
                if (c == '\\' && *p == 'n') { c = '\n'; p++; }
                if (out + 1 >= size) return -1;
                challenge[out++] = c;
            }
            challenge[out] = '\0';
            return out > 0 ? 0 : -1;
        }
        line = strchr(line, '\n');
        if (line != NULL) line++;
    }
    return -1;
}

static int is_tunnel_user(pam_handle_t *pamh)
{
    const char *user = NULL;
    if (pam_get_user(pamh, &user, NULL) != PAM_SUCCESS || user == NULL) return 0;
    return strcmp(user, TUNNEL_USER) == 0;
}

int pam_sm_authenticate(pam_handle_t *pamh, int flags, int argc, const char **argv)
{
    struct settings s = parse_settings(argc, argv);
    char reply[MAX_REPLY];
    char challenge[MAX_FIELD];
    char request[2 * MAX_FIELD + 2];
    char *answer = NULL;
    size_t challenge_len, answer_len;
    int result = PAM_AUTH_ERR;

    (void)flags;
    if (!is_tunnel_user(pamh)) {
        pam_syslog(pamh, LOG_ERR, "refused: this service is only for %s", TUNNEL_USER);
        return PAM_AUTH_ERR;
    }
    if (run_helper(pamh, &s, "challenge", NULL, 0, reply, sizeof reply) != 0) return PAM_AUTHINFO_UNAVAIL;
    if (parse_challenge(reply, challenge, sizeof challenge) != 0) {
        pam_syslog(pamh, LOG_ERR, "FileES authentication helper returned no challenge");
        return PAM_AUTHINFO_UNAVAIL;
    }
    if (pam_prompt(pamh, PAM_PROMPT_ECHO_OFF, &answer, "%s", challenge) != PAM_SUCCESS || answer == NULL) {
        return PAM_AUTH_ERR;
    }
    challenge_len = strlen(challenge);
    answer_len = strlen(answer);
    if (answer_len == 0 || answer_len >= MAX_FIELD) goto done;

    /* The helper reads two NUL-terminated fields: challenge, then response. */
    memcpy(request, challenge, challenge_len + 1);
    memcpy(request + challenge_len + 1, answer, answer_len + 1);
    if (run_helper(pamh, &s, "response", request, challenge_len + answer_len + 2, reply, sizeof reply) != 0) {
        result = PAM_AUTHINFO_UNAVAIL;
        goto done;
    }
    /* Exactly "authorize": anything else, including extra output, is a refusal. */
    if (strcmp(reply, "authorize\n") == 0) result = PAM_SUCCESS;

done:
    wipe(request, sizeof request);
    if (answer != NULL) {
        wipe(answer, strlen(answer));
        free(answer);
    }
    return result;
}

int pam_sm_setcred(pam_handle_t *pamh, int flags, int argc, const char **argv)
{
    (void)pamh; (void)flags; (void)argc; (void)argv;
    return PAM_SUCCESS;
}

/*
 * The account is a locked system account with no password and no expiry
 * policy; pam_unix would have nothing meaningful to say about it. Access is
 * decided by authentication above and by sshd's Match block.
 */
int pam_sm_acct_mgmt(pam_handle_t *pamh, int flags, int argc, const char **argv)
{
    (void)flags; (void)argc; (void)argv;
    return is_tunnel_user(pamh) ? PAM_SUCCESS : PAM_PERM_DENIED;
}

int pam_sm_open_session(pam_handle_t *pamh, int flags, int argc, const char **argv)
{
    (void)flags; (void)argc; (void)argv;
    return is_tunnel_user(pamh) ? PAM_SUCCESS : PAM_SESSION_ERR;
}

int pam_sm_close_session(pam_handle_t *pamh, int flags, int argc, const char **argv)
{
    (void)pamh; (void)flags; (void)argc; (void)argv;
    return PAM_SUCCESS;
}
