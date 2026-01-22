#ifndef PIPEWIRE_MUTE_H
#define PIPEWIRE_MUTE_H

#include <stdint.h>
#include <stdbool.h>

int pw_mute_init(void);
void pw_mute_set(uint32_t node_id, bool mute);
void pw_mute_deinit(void);

#endif
