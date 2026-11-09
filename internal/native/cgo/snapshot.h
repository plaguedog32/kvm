#ifndef JETKVM_SNAPSHOT_H
#define JETKVM_SNAPSHOT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

// One-shot raw frame capture. The caller owns *data and must free it.
// Returns an errno value; never returns a cached frame.
int video_snapshot(uint8_t **data, size_t *length, uint32_t *width,
                   uint32_t *height, unsigned timeout_ms);
bool video_snapshot_requested(void);
void video_snapshot_submit(const uint8_t *data, size_t length, uint32_t width,
                           uint32_t height, uint32_t stride);

#endif
