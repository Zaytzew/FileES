/*
 * Drives pam_filees_tunnel.so through real libpam with a private
 * configuration directory, so nothing under /etc is read or written.
 * usage: harness <confdir> <user> <answer> [capture-prompt-file]
 * exit: 0 authenticated and account ok, 1 refused, 2 unavailable/other.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <security/pam_appl.h>

static const char *answer;
static const char *capture;

static int conv(int n, const struct pam_message **msg, struct pam_response **resp, void *data)
{
    struct pam_response *r = calloc((size_t)n, sizeof *r);
    int i;
    (void)data;
    if (r == NULL) return PAM_BUF_ERR;
    for (i = 0; i < n; i++) {
        if (msg[i]->msg_style == PAM_PROMPT_ECHO_OFF || msg[i]->msg_style == PAM_PROMPT_ECHO_ON) {
            if (capture != NULL) {
                FILE *f = fopen(capture, "w");
                if (f != NULL) { fputs(msg[i]->msg, f); fclose(f); }
            }
            r[i].resp = strdup(answer);
        }
    }
    *resp = r;
    return PAM_SUCCESS;
}

int main(int argc, char **argv)
{
    struct pam_conv c = { conv, NULL };
    pam_handle_t *h = NULL;
    int rc;
    if (argc < 4) return 2;
    answer = argv[3];
    capture = argc > 4 ? argv[4] : NULL;
    if (pam_start_confdir("filees-tunnel", argv[2], &c, argv[1], &h) != PAM_SUCCESS) return 2;
    rc = pam_authenticate(h, 0);
    if (rc == PAM_SUCCESS) rc = pam_acct_mgmt(h, 0);
    pam_end(h, rc);
    if (rc == PAM_SUCCESS) return 0;
    if (rc == PAM_AUTH_ERR || rc == PAM_PERM_DENIED) return 1;
    return 2;
}
