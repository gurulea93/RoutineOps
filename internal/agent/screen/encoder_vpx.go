//go:build screen && vpx && cgo

package screen

// VP8 через libvpx (Этап B, шаг B1). Отдельный тег vpx, а не просто cgo: CGO на macOS
// включён ради захвата и ввода, и libvpx не должен становиться обязательной зависимостью
// всякой CGO-сборки. Без тега кодек не регистрируется — агент остаётся на RSF1.
//
// Сборка: libvpx (>= 1.8) видна через pkg-config; для статической линковки —
// PKG_CONFIG="pkg-config --static". См. docs/libvpx-build.md.

/*
#cgo pkg-config: vpx
#include <stdlib.h>
#include <string.h>
#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>

typedef struct {
	vpx_codec_ctx_t     ctx;
	vpx_codec_enc_cfg_t cfg;
	vpx_image_t         img;
	vpx_codec_pts_t     pts;
	int                 inited;
	int                 img_alloc;
} rops_vp8;

// vpx_codec_enc_init и vpx_codec_control — макросы (второй ещё и вариадический), cgo их
// не вызывает, поэтому вся работа с контекстом — в C-обёртках.
static vpx_codec_err_t rops_vp8_open(rops_vp8 *e, int w, int h, int fps, int kbps, int threads) {
	vpx_codec_iface_t *iface = vpx_codec_vp8_cx();
	vpx_codec_err_t err = vpx_codec_enc_config_default(iface, &e->cfg, 0);
	if (err) return err;

	e->cfg.g_w = w;
	e->cfg.g_h = h;
	e->cfg.g_timebase.num = 1;
	e->cfg.g_timebase.den = fps;
	e->cfg.g_threads = threads;
	e->cfg.g_pass = VPX_RC_ONE_PASS;
	e->cfg.g_lag_in_frames = 0;           // ни кадра задержки: это интерактив, а не VOD
	e->cfg.g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
	e->cfg.rc_end_usage = VPX_CBR;
	e->cfg.rc_target_bitrate = kbps;
	e->cfg.rc_min_quantizer = 4;
	e->cfg.rc_max_quantizer = 56;
	e->cfg.rc_undershoot_pct = 100;
	e->cfg.rc_overshoot_pct = 15;
	e->cfg.rc_buf_initial_sz = 500;
	e->cfg.rc_buf_optimal_sz = 600;
	e->cfg.rc_buf_sz = 1000;
	e->cfg.kf_mode = VPX_KF_AUTO;
	e->cfg.kf_max_dist = fps * 10;        // периодический ключевой кадр — страховка от потерь без PLI

	err = vpx_codec_enc_init(&e->ctx, iface, &e->cfg, 0);
	if (err) return err;
	e->inited = 1;

	vpx_codec_control(&e->ctx, VP8E_SET_CPUUSED, 8);
	vpx_codec_control(&e->ctx, VP8E_SET_SCREEN_CONTENT_MODE, 1); // tune-content=screen
	vpx_codec_control(&e->ctx, VP8E_SET_STATIC_THRESHOLD, 1);
	vpx_codec_control(&e->ctx, VP8E_SET_NOISE_SENSITIVITY, 0);
	vpx_codec_control(&e->ctx, VP8E_SET_TOKEN_PARTITIONS, VP8_ONE_TOKENPARTITION);
	vpx_codec_control(&e->ctx, VP8E_SET_MAX_INTRA_BITRATE_PCT, 300);

	if (!vpx_img_alloc(&e->img, VPX_IMG_FMT_I420, w, h, 16)) return VPX_CODEC_MEM_ERROR;
	e->img_alloc = 1;
	return VPX_CODEC_OK;
}

static void rops_vp8_fill(rops_vp8 *e, const unsigned char *y, int ys,
                          const unsigned char *u, const unsigned char *v, int uvs) {
	int w = (int)e->cfg.g_w, h = (int)e->cfg.g_h;
	int cw = (w + 1) / 2, ch = (h + 1) / 2, r;
	for (r = 0; r < h; r++)
		memcpy(e->img.planes[VPX_PLANE_Y] + r * e->img.stride[VPX_PLANE_Y], y + r * ys, w);
	for (r = 0; r < ch; r++) {
		memcpy(e->img.planes[VPX_PLANE_U] + r * e->img.stride[VPX_PLANE_U], u + r * uvs, cw);
		memcpy(e->img.planes[VPX_PLANE_V] + r * e->img.stride[VPX_PLANE_V], v + r * uvs, cw);
	}
}

static vpx_codec_err_t rops_vp8_encode(rops_vp8 *e, int force_key) {
	vpx_enc_frame_flags_t flags = force_key ? VPX_EFLAG_FORCE_KF : 0;
	vpx_codec_err_t err = vpx_codec_encode(&e->ctx, &e->img, e->pts, 1, flags, VPX_DL_REALTIME);
	e->pts++;
	return err;
}

// Следующий пакет кадра; 0 — пакеты кончились. Указатель живёт до следующего encode.
static int rops_vp8_next(rops_vp8 *e, vpx_codec_iter_t *it, const void **buf, size_t *sz) {
	const vpx_codec_cx_pkt_t *p;
	while ((p = vpx_codec_get_cx_data(&e->ctx, it)) != NULL) {
		if (p->kind == VPX_CODEC_CX_FRAME_PKT) {
			*buf = p->data.frame.buf;
			*sz = p->data.frame.sz;
			return 1;
		}
	}
	return 0;
}

static vpx_codec_err_t rops_vp8_bitrate(rops_vp8 *e, unsigned int kbps) {
	e->cfg.rc_target_bitrate = kbps;
	return vpx_codec_enc_config_set(&e->ctx, &e->cfg);
}

static const char *rops_vp8_detail(rops_vp8 *e) {
	const char *d = e->inited ? vpx_codec_error_detail(&e->ctx) : NULL;
	return d ? d : "";
}

static void rops_vp8_close(rops_vp8 *e) {
	if (e->img_alloc) vpx_img_free(&e->img);
	if (e->inited) vpx_codec_destroy(&e->ctx);
	free(e);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func init() { registerEncoder("vp8", newVP8Encoder) }

type vp8Encoder struct {
	e   *C.rops_vp8 // C-память: в структуре указатели libvpx, Go-куче их не отдаём
	cfg EncoderConfig
}

func newVP8Encoder(cfg EncoderConfig) (Encoder, error) {
	e := (*C.rops_vp8)(C.calloc(1, C.size_t(unsafe.Sizeof(C.rops_vp8{}))))
	if e == nil {
		return nil, fmt.Errorf("screen: vp8: out of memory")
	}
	if rc := C.rops_vp8_open(e, C.int(cfg.Width), C.int(cfg.Height), C.int(cfg.FPS),
		C.int(cfg.BitrateKbps), C.int(cfg.Threads)); rc != C.VPX_CODEC_OK {
		err := vpxError("init", e, rc)
		C.rops_vp8_close(e)
		return nil, err
	}
	return &vp8Encoder{e: e, cfg: cfg}, nil
}

func (v *vp8Encoder) Encode(f *I420, forceKey bool) ([]byte, error) {
	if v.e == nil {
		return nil, fmt.Errorf("screen: vp8: encoder closed")
	}
	if f.Width != v.cfg.Width || f.Height != v.cfg.Height {
		return nil, fmt.Errorf("screen: vp8: frame %dx%d, encoder %dx%d", f.Width, f.Height, v.cfg.Width, v.cfg.Height)
	}
	cw, ch := (f.Width+1)/2, (f.Height+1)/2
	if f.YStride < f.Width || f.UVStride < cw ||
		len(f.Y) < f.YStride*(f.Height-1)+f.Width ||
		len(f.U) < f.UVStride*(ch-1)+cw || len(f.V) < f.UVStride*(ch-1)+cw {
		return nil, fmt.Errorf("screen: vp8: i420 planes too small for %dx%d", f.Width, f.Height)
	}
	C.rops_vp8_fill(v.e,
		(*C.uchar)(unsafe.Pointer(&f.Y[0])), C.int(f.YStride),
		(*C.uchar)(unsafe.Pointer(&f.U[0])), (*C.uchar)(unsafe.Pointer(&f.V[0])), C.int(f.UVStride))

	force := C.int(0)
	if forceKey {
		force = 1
	}
	if rc := C.rops_vp8_encode(v.e, force); rc != C.VPX_CODEC_OK {
		return nil, vpxError("encode", v.e, rc)
	}

	var (
		out []byte
		it  C.vpx_codec_iter_t
		buf unsafe.Pointer
		sz  C.size_t
	)
	for C.rops_vp8_next(v.e, &it, &buf, &sz) != 0 {
		out = append(out, C.GoBytes(buf, C.int(sz))...)
	}
	return out, nil
}

func (v *vp8Encoder) SetBitrate(kbps int) error {
	if v.e == nil {
		return fmt.Errorf("screen: vp8: encoder closed")
	}
	if kbps <= 0 {
		return fmt.Errorf("screen: vp8: bad bitrate %d", kbps)
	}
	if rc := C.rops_vp8_bitrate(v.e, C.uint(kbps)); rc != C.VPX_CODEC_OK {
		return vpxError("set bitrate", v.e, rc)
	}
	v.cfg.BitrateKbps = kbps
	return nil
}

func (v *vp8Encoder) Close() {
	if v.e != nil {
		C.rops_vp8_close(v.e)
		v.e = nil
	}
}

func vpxError(op string, e *C.rops_vp8, rc C.vpx_codec_err_t) error {
	msg := C.GoString(C.vpx_codec_err_to_string(rc))
	if d := C.GoString(C.rops_vp8_detail(e)); d != "" {
		msg += ": " + d
	}
	return fmt.Errorf("screen: vp8 %s: %s", op, msg)
}
