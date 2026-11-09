#define _POSIX_C_SOURCE 200809L
#include "snapshot.h"
#include <assert.h>
#include <errno.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static void *submit(void *unused)
{
    (void)unused;
    struct timespec interval = {0, 1000000};
    while (!video_snapshot_requested()) nanosleep(&interval, NULL);
    // Two padded rows. The returned buffer must strip padding.
    uint8_t raw[] = {16,128,235,128,99,99,99,99,20,128,30,128};
    video_snapshot_submit(raw, sizeof(raw), 2, 2, 8);
    memset(raw, 0, sizeof(raw)); // Returned pixels must own their storage.
    return NULL;
}

int main(void)
{
    uint8_t *data = NULL;
    size_t length = 0;
    uint32_t width, height;
    // A frame submitted without a request must never become a cached snapshot.
    video_snapshot_submit((uint8_t[]){1,2,3,4},4,2,1,4);
    assert(video_snapshot(&data,&length,&width,&height,10) == ETIMEDOUT);
    assert(data == NULL);
    pthread_t producer;
    assert(pthread_create(&producer,NULL,submit,NULL) == 0);
    assert(video_snapshot(&data,&length,&width,&height,1000) == 0);
    pthread_join(producer,NULL);
    assert(width == 2 && height == 2 && length == 8);
    assert(memcmp(data,(uint8_t[]){16,128,235,128,20,128,30,128},8) == 0);
    free(data);
    assert(video_snapshot(&data,&length,&width,&height,10) == ETIMEDOUT);
    return 0;
}
