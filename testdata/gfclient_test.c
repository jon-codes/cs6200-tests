#define _GNU_SOURCE

#include <ctype.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "gf-student.h"
#include "gfclient-student.h"

#if INT_WIDTH != 32
#error "Expected int type to be 32 bits wide."
#endif

#if SIZE_WIDTH != 64
#error "Expected size_t type to be 64 bits wide."
#endif

#define HEADER_MAX_SIZE 5120
#define DEFAULT_BUF_CAP 1024
#define DEFAULT_BUF_FACTOR 2
#define EXIT_USAGE 2

#define fatalf(msg, ...)                            \
    do {                                            \
        fprintf(stderr, "%s:%d: " msg "\n",         \
                __FILE__, __LINE__, ##__VA_ARGS__); \
        exit(EXIT_FAILURE);                         \
    } while (0)

#define fatalf_code(code, msg, ...)                 \
    do {                                            \
        fprintf(stderr, "%s:%d: " msg "\n",         \
                __FILE__, __LINE__, ##__VA_ARGS__); \
        exit(code);                                 \
    } while (0)

#define DEFAULT_CAPACITY 16
#define DEFAULT_GROWTH_FACTOR 2

typedef __uint128_t uint128_t;

struct Buffer {
    char *data;
    size_t size;
    size_t capacity;
    int growth_factor;
};

typedef struct Buffer Buffer;

static Buffer *buf_create(size_t init_capacity, int growth_factor) {
    Buffer *buf = malloc(sizeof(Buffer));
    if (!buf) {
        return NULL;
    }

    buf->data = malloc(init_capacity);
    if (!buf->data) {
        free(buf);
        return NULL;
    }

    buf->size = 0;
    buf->capacity = init_capacity;
    buf->growth_factor = growth_factor;
    return buf;
}

static bool buf_ensure_capacity(Buffer *buf, size_t to_add) {
    if (!buf) {
        return false;
    }

    size_t required_size = buf->size + to_add;
    if (required_size <= buf->capacity) {
        return true;  // already enough room
    }

    // calculate new capacity
    size_t new_capacity = buf->capacity;
    while (new_capacity < required_size) {
        new_capacity *= buf->growth_factor;
        // check for overflow:
        if (new_capacity < buf->capacity) {
            return false;
        }
    }

    // resize the buffer
    char *new_data = (char *)realloc(buf->data, new_capacity);
    if (!new_data) {
        return false;
    }

    buf->data = new_data;
    buf->capacity = new_capacity;
    return true;
}

static bool buf_append(Buffer *buf, const void *data, size_t length) {
    if (!buf || !data || length == 0) {
        return false;
    }

    if (!buf_ensure_capacity(buf, length)) {
        return false;
    }

    memcpy(buf->data + buf->size, data, length);
    buf->size += length;
    return true;
}

size_t buf_size(const Buffer *buf) {
    return buf ? buf->size : 0;
}

static void *buf_data(const Buffer *buf) {
    return buf ? buf->data : NULL;
}

static void buf_destroy(Buffer *buf) {
    if (buf) {
        free(buf->data);
        free(buf);
    }
}

struct Message {
    int32_t return_code;
    int32_t status;
    uint64_t bytes_received;
    uint64_t file_len;
    void *header;
    uint64_t header_size;
    void *data;
    uint64_t data_size;
};

typedef struct Message Message;

static void serialize_int32(int32_t value, FILE *f) {
    fwrite(&value, 1, sizeof(value), f);
    fflush(f);
}

static void serialize_uint64(uint64_t value, FILE *f) {
    fwrite(&value, 1, sizeof(value), f);
    fflush(f);
}

static void serialize_bytes(void *data, size_t data_len, FILE *f) {
    serialize_uint64(data_len, f);
    fwrite(data, 1, data_len, f);
    fflush(f);
}

static void serialize_message(Message *msg, FILE *f) {
    serialize_int32(msg->return_code, f);
    serialize_int32(msg->status, f);
    serialize_uint64(msg->bytes_received, f);
    serialize_uint64(msg->file_len, f);
    serialize_bytes(msg->header, msg->header_size, f);
    serialize_bytes(msg->data, msg->data_size, f);
}

static void fprint_str_escaped(FILE *outf, const char *str) {
    while (*str) {
        unsigned char ch = *str++;
        switch (ch) {
            case '\"':
                fputs("\\\"", outf);
                break;
            case '\'':
                fputs("\\\'", outf);
                break;
            case '\\':
                fputs("\\\\", outf);
                break;
            case '\a':
                fputs("\\a", outf);
                break;
            case '\b':
                fputs("\\b", outf);
                break;
            case '\n':
                fputs("\\n", outf);
                break;
            case '\t':
                fputs("\\t", outf);
                break;
            default:
                if (iscntrl(ch))
                    fprintf(outf, "\\%03o", ch);
                else
                    fputc(ch, outf);
        }
    }
}

void print_help(const char *program) {
    printf("usage: %s -help\n", program);
    printf("       %s [-addr ADDR] [-port PORT]\n\n", program);
    printf("test harness for gfclient :)\n\n");
    printf("options:\n");
    printf("  -h -help             show this help and exit\n");
    printf("  -a ADDR -addr=ADDR   server host (default: 127.0.0.1)\n");
    printf("  -p PORT -port=PORT   server port (default: 1234)\n\n");
    printf("environment:\n");
    printf("  DEBUG   enable verbose logging to STDERR\n");
}

struct Config {
    const char *addr;
    const char *port;
    bool debug;
};

typedef struct Config Config;

static void headercb(void *data, size_t data_size, void *buf) {
    if (data_size >= HEADER_MAX_SIZE) {
        fatalf("headercb: data_len %ld exceeds max header size %d", data_size, HEADER_MAX_SIZE);
    }

    memcpy(buf, data, data_size);
    ((char *)buf)[data_size] = '\0';
}

static void writecb(void *data, size_t data_size, void *buf) {
    buf_append(buf, data, data_size);  // TODO: error handling?
}

int main(int argc, char *argv[]) {
    char *debug_env = getenv("DEBUG");

    Config conf = {
        .addr = "127.0.0.1",
        .port = "1234",
        .debug = debug_env != NULL &&
                 strcmp(debug_env, "0") != 0 &&
                 strcmp(debug_env, "false") != 0 &&
                 strcmp(debug_env, "FALSE") != 0,
    };

    static struct option long_options[] = {
        {"help", no_argument, 0, 'h'},
        {"addr", required_argument, 0, 'a'},
        {"port", required_argument, 0, 'p'},
        {0, 0, 0, 0},
    };

    setbuf(stdout, NULL);
    setbuf(stderr, NULL);

    int opt;
    while ((opt = getopt_long_only(argc, argv, "ha:p:", long_options, NULL)) != -1) {
        switch (opt) {
            case 'h':
                print_help(argv[0]);
                return EXIT_SUCCESS;
                break;
            case 'a':
                conf.addr = optarg;
                break;
            case 'p':
                conf.port = optarg;
                break;
            case '?':
                fprintf(stderr, "run '%s -h' for usage\n", argv[0]);
                return EXIT_USAGE;

            default:
                abort();
        }
    }

    if (optind < argc) {
        fprintf(stderr, "%s: unexpected argument --", argv[0]);
        for (int i = optind; i < argc; i++) {
            fprintf(stderr, " \"%s\"", argv[i]);
        }
        fprintf(stderr, "\n");
        fprintf(stderr, "run '%s -h' for usage\n", argv[0]);
        return EXIT_USAGE;
    }

    if (conf.debug) {
        fprintf(stderr, "conf.addr  = \"%s\"\n", conf.addr);
        fprintf(stderr, "conf.port  = \"%s\"\n", conf.port);
        fprintf(stderr, "conf.debug = %s\n", conf.debug ? "true" : "false");
    }

    if (conf.debug) fprintf(stderr, "gfc_global_init\n");
    gfc_global_init();

    char *path = NULL;
    size_t path_size = 0;
    ssize_t read;

    if (conf.debug) fprintf(stderr, "reading path line ...\n");

    while ((read = getline(&path, &path_size, stdin)) != -1) {
        if (read > 0 && path[read - 1] == '\n') {
            path[read - 1] = '\0';
        }

        if (conf.debug) {
            fprintf(stderr, "read path \"");
            fprint_str_escaped(stderr, path);
            fprintf(stderr, "\"\n");
        }

        gfcrequest_t *gfr = NULL;

        if (conf.debug) fprintf(stderr, "gfc_create\n");
        gfr = gfc_create();

        int port_num = strtol(conf.port, (char **)NULL, 10);
        if (conf.debug) fprintf(stderr, "gfc_set_port: %d\n", port_num);
        gfc_set_port(&gfr, port_num);

        if (conf.debug) {
            fprintf(stderr, "gfc_set_server: \"");
            fprint_str_escaped(stderr, conf.addr);
            fprintf(stderr, "\")\n");
        }
        gfc_set_server(&gfr, conf.addr);

        if (conf.debug) {
            fprintf(stderr, "gfc_set_path: \"");
            fprint_str_escaped(stderr, path);
            fprintf(stderr, "\")\n");
            gfc_set_path(&gfr, path);
        }

        char header_buf[HEADER_MAX_SIZE];
        header_buf[0] = '\0';

        if (conf.debug) fprintf(stderr, "gfc_set_headerfunc\n");
        gfc_set_headerfunc(&gfr, headercb);
        if (conf.debug) fprintf(stderr, "gfc_set_headerarg\n");
        gfc_set_headerarg(&gfr, header_buf);

        Buffer *data_buf = buf_create(DEFAULT_BUF_CAP, DEFAULT_BUF_FACTOR);
        if (data_buf == NULL) {
            fatalf("buf_create: %s", strerror(errno));  // malloc error
        }

        if (conf.debug) fprintf(stderr, "gfc_set_writefunc\n");
        gfc_set_writefunc(&gfr, writecb);
        if (conf.debug) fprintf(stderr, "gfc_set_writearg\n");
        gfc_set_writearg(&gfr, data_buf);

        if (conf.debug) fprintf(stderr, "gfc_perform\n");
        int return_code = gfc_perform(&gfr);
        if (conf.debug) fprintf(stderr, "-> %d\n", return_code);

        if (conf.debug) fprintf(stderr, "gfc_get_status\n");
        gfstatus_t status = gfc_get_status(&gfr);
        const char *status_str = gfc_strstatus(status);
        if (conf.debug) fprintf(stderr, "-> %d (%s)\n", status, status_str);

        if (conf.debug) fprintf(stderr, "gfc_get_bytesreceived\n");
        size_t bytes_received = gfc_get_bytesreceived(&gfr);
        if (conf.debug) fprintf(stderr, "-> %lu\n", bytes_received);

        if (conf.debug) fprintf(stderr, "gfc_get_filelen\n");
        size_t file_len = gfc_get_filelen(&gfr);
        if (conf.debug) fprintf(stderr, "-> %lu\n", file_len);

        Message msg = {
            .return_code = return_code,
            .status = status,
            .file_len = file_len,
            .bytes_received = bytes_received,
            .header = header_buf,
            .header_size = strlen(header_buf),
            .data = buf_data(data_buf),
            .data_size = buf_size(data_buf),
        };

        if (conf.debug) {
            fprintf(stderr, "msg.return_code        = %d\n", msg.return_code);
            fprintf(stderr, "msg.status             = %d\n", msg.status);
            fprintf(stderr, "msg.file_len           = %ld\n", msg.file_len);
            fprintf(stderr, "msg.bytes_received     = %ld\n", msg.bytes_received);
            fprintf(stderr, "msg.header_size        = %ld\n", msg.header_size);
            fprintf(stderr, "msg.data_size          = %ld\n", msg.data_size);
        }

        serialize_message(&msg, stdout);

        buf_destroy(data_buf);

        if (conf.debug) fprintf(stderr, "gfc_cleanup\n");
        gfc_cleanup(&gfr);

        if (conf.debug) fprintf(stderr, "reading path line ...\n");
    }

    if (conf.debug) fprintf(stderr, "read EOF\n");

    if (conf.debug) fprintf(stderr, "gfc_global_cleanup\n");
    gfc_global_cleanup();

    return EXIT_SUCCESS;
}
