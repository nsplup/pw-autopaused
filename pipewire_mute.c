#include "pipewire_mute.h"
#include <pipewire/pipewire.h>
#include <pipewire/thread-loop.h>
#include <spa/param/props.h>
#include <spa/utils/result.h>
#include <spa/utils/string.h>
#include <spa/pod/builder.h>
#include <spa/param/audio/raw.h>

#include <stdio.h>
#include <stdlib.h>

struct pw_data {
    struct pw_thread_loop *loop;
    struct pw_context *context;
    struct pw_core *core;
    struct pw_registry *registry;
    struct spa_hook core_listener;
    struct spa_hook registry_listener;
    
    int pending_seq;
    bool done;
};

static struct pw_data data = {0};

static void on_core_error(void *userdata, uint32_t id, int seq, int res, const char *message)
{
    pw_log_error("core error: id:%u seq:%d res:%d (%s): %s",
                 id, seq, res, spa_strerror(res), message);
    
    if (res == -EPIPE || id == PW_ID_CORE) {
        fprintf(stderr, "PipeWire connection lost (EPIPE). Exiting immediately.\n");
        _Exit(1);
    }
}

static const struct pw_core_events core_events = {
    .version = PW_VERSION_CORE_EVENTS,
    .error = on_core_error,
};

static const struct pw_registry_events registry_events = {
    .version = PW_VERSION_REGISTRY_EVENTS,
};

static void on_node_param(void *userdata, int seq, uint32_t id, uint32_t index, uint32_t next, const struct spa_pod *param)
{
    (void)id; (void)index; (void)next; (void)param; // 未使用参数

    struct pw_data *d = userdata;
    if (seq == d->pending_seq) {
        d->done = true;
        pw_thread_loop_signal(d->loop, false);
    }
}

static const struct pw_node_events node_events = {
    .version = PW_VERSION_NODE_EVENTS,
    .param = on_node_param,
};

int pw_mute_init(void)
{
    pw_init(NULL, NULL);

    data.loop = pw_thread_loop_new("pw-mute-loop", NULL);
    if (!data.loop) {
        fprintf(stderr, "Failed to create thread loop\n");
        return -1;
    }

    data.context = pw_context_new(pw_thread_loop_get_loop(data.loop), NULL, 0);
    if (!data.context) {
        fprintf(stderr, "Failed to create context\n");
        return -1;
    }

    data.core = pw_context_connect(data.context, NULL, 0);
    if (!data.core) {
        fprintf(stderr, "Failed to connect to core\n");
        return -1;
    }

    pw_core_add_listener(data.core, &data.core_listener, &core_events, &data);

    data.registry = pw_core_get_registry(data.core, PW_VERSION_REGISTRY, 0);
    if (!data.registry) {
        fprintf(stderr, "Failed to get registry\n");
        return -1;
    }
    pw_registry_add_listener(data.registry, &data.registry_listener, &registry_events, &data);

    if (pw_thread_loop_start(data.loop) != 0) {
        fprintf(stderr, "Failed to start thread loop\n");
        return -1;
    }

    return 0;
}

void pw_mute_set(uint32_t node_id, bool mute)
{
    struct pw_proxy *proxy = NULL;
    struct spa_hook listener;
    
    pw_thread_loop_lock(data.loop);

    proxy = pw_registry_bind(data.registry, node_id, PW_TYPE_INTERFACE_Node, PW_VERSION_NODE, 0);
    if (!proxy) {
        pw_log_error("Failed to bind to node %u", node_id);
        goto out;
    }

    pw_node_add_listener((struct pw_node*)proxy, &listener, &node_events, &data);

    uint8_t buffer[1024];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    
    float volume = mute ? 0.0f : 1.0f;
    float volumes[SPA_AUDIO_MAX_CHANNELS]; 
    for(int i=0; i<SPA_AUDIO_MAX_CHANNELS; i++) volumes[i] = volume;

    struct spa_pod_frame f;
    spa_pod_builder_push_object(&b, &f, SPA_TYPE_OBJECT_Props, SPA_PARAM_Props);
    spa_pod_builder_prop(&b, SPA_PROP_channelVolumes, 0);
    spa_pod_builder_array(&b, sizeof(float), SPA_TYPE_Float, 8, volumes);
    const struct spa_pod *param = spa_pod_builder_pop(&b, &f);

    data.done = false;
    data.pending_seq = pw_node_set_param((struct pw_node*)proxy, SPA_PARAM_Props, 0, param);

    while (!data.done) {
        int r = pw_thread_loop_timed_wait(data.loop, 2);
        if (r != 0) {
            pw_log_warn("set param timeout or error for node %u", node_id);
            break;
        }
    }

    spa_hook_remove(&listener);
    pw_proxy_destroy(proxy);

out:
    pw_thread_loop_unlock(data.loop);
}

void pw_mute_deinit(void)
{
    if (data.loop) {
        pw_thread_loop_stop(data.loop);
    }
    if (data.registry) {
        spa_hook_remove(&data.registry_listener);
        pw_proxy_destroy((struct pw_proxy*)data.registry);
        data.registry = NULL;
    }
    if (data.core) {
        spa_hook_remove(&data.core_listener);
        pw_core_disconnect(data.core);
        data.core = NULL;
    }
    if (data.context) {
        pw_context_destroy(data.context);
        data.context = NULL;
    }
    if (data.loop) {
        pw_thread_loop_destroy(data.loop);
        data.loop = NULL;
    }
    pw_deinit();
}
