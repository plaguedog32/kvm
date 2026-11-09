#define _POSIX_C_SOURCE 200809L
#include "snapshot.h"
#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static pthread_mutex_t snapshot_mutex = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t snapshot_cond;
static pthread_once_t snapshot_once = PTHREAD_ONCE_INIT;
static int snapshot_init_error;
static atomic_bool snapshot_pending = false;
static bool snapshot_busy;
static uint8_t *snapshot_data;
static size_t snapshot_length;
static uint32_t snapshot_width, snapshot_height;
static int snapshot_error;

static void snapshot_init(void)
{
    pthread_condattr_t attr;
    snapshot_init_error = pthread_condattr_init(&attr);
    if (snapshot_init_error != 0) return;
    snapshot_init_error = pthread_condattr_setclock(&attr, CLOCK_MONOTONIC);
    if (snapshot_init_error == 0)
        snapshot_init_error = pthread_cond_init(&snapshot_cond, &attr);
    pthread_condattr_destroy(&attr);
}

bool video_snapshot_requested(void)
{
    return atomic_load(&snapshot_pending);
}

int video_snapshot(uint8_t **data, size_t *length, uint32_t *width,
                   uint32_t *height, unsigned timeout_ms)
{
    if (!data || !length || !width || !height || timeout_ms == 0 || timeout_ms > 3000)
        return EINVAL;
    *data = NULL;
    *length = 0;
    pthread_once(&snapshot_once, snapshot_init);
    if (snapshot_init_error != 0) return snapshot_init_error;
    pthread_mutex_lock(&snapshot_mutex);
    if (snapshot_busy) {
        pthread_mutex_unlock(&snapshot_mutex);
        return EBUSY;
    }
    snapshot_busy = true;
    struct timespec deadline;
    clock_gettime(CLOCK_MONOTONIC, &deadline);
    deadline.tv_sec += timeout_ms / 1000;
    deadline.tv_nsec += (timeout_ms % 1000) * 1000000L;
    if (deadline.tv_nsec >= 1000000000L) {
        deadline.tv_sec++;
        deadline.tv_nsec -= 1000000000L;
    }
    snapshot_error = 0;
    atomic_store(&snapshot_pending, true);
    int ret = 0;
    while (atomic_load(&snapshot_pending) && ret == 0)
        ret = pthread_cond_timedwait(&snapshot_cond, &snapshot_mutex, &deadline);
    atomic_store(&snapshot_pending, false);
    if (ret == 0) ret = snapshot_error;
    if (ret == 0 && snapshot_data) {
        *data = snapshot_data;
        *length = snapshot_length;
        *width = snapshot_width;
        *height = snapshot_height;
    } else {
        free(snapshot_data);
        if (ret == 0) ret = EIO;
    }
    snapshot_data = NULL;
    snapshot_busy = false;
    pthread_mutex_unlock(&snapshot_mutex);
    return ret;
}

void video_snapshot_submit(const uint8_t *data, size_t length, uint32_t width,
                           uint32_t height, uint32_t stride)
{
    if (!atomic_load(&snapshot_pending)) return;
    pthread_mutex_lock(&snapshot_mutex);
    if (!atomic_load(&snapshot_pending)) {
        pthread_mutex_unlock(&snapshot_mutex);
        return;
    }
    if (!data || width == 0 || height == 0 || width > 4096 || height > 2160 ||
        width % 2 != 0 || stride < width * 2 ||
        (size_t)stride * (height - 1) + width * 2 > length) {
        snapshot_error = EINVAL;
    } else {
        snapshot_length = (size_t)width * height * 2;
        snapshot_data = malloc(snapshot_length);
        if (!snapshot_data) snapshot_error = ENOMEM;
        else {
            for (uint32_t row = 0; row < height; row++)
                memcpy(snapshot_data + (size_t)row * width * 2,
                       data + (size_t)row * stride, width * 2);
            snapshot_width = width;
            snapshot_height = height;
        }
    }
    atomic_store(&snapshot_pending, false);
    pthread_cond_signal(&snapshot_cond);
    pthread_mutex_unlock(&snapshot_mutex);
}
