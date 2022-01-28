//go:build !purego

#include "textflag.h"

#define PRIME1 0x9E3779B185EBCA87
#define PRIME2 0xC2B2AE3D27D4EB4F
#define PRIME3 0x165667B19E3779F9
#define PRIME4 0x85EBCA77C2B2AE63
#define PRIME5 0x27D4EB2F165667C5

#define prime1 R12
#define prime2 R13
#define prime3 R14
#define prime4 R15
#define prime5 R15 // same as prime4 because they are not used together

#define prime1YMM Y12
#define prime2YMM Y13
#define prime3YMM Y14
#define prime4YMM Y15
#define prime5YMM Y15

#define tmp1YMM Y6
#define tmp2YMM Y7
#define tmp3YMM Y8
#define tmp4YMM Y9
#define tmp5YMM Y10
#define tmp6YMM Y11

DATA prime1vec<>+0(SB)/8, $PRIME1
DATA prime1vec<>+8(SB)/8, $PRIME1
DATA prime1vec<>+16(SB)/8, $PRIME1
DATA prime1vec<>+24(SB)/8, $PRIME1
GLOBL prime1vec<>(SB), RODATA|NOPTR, $32

DATA prime2vec<>+0(SB)/8, $PRIME2
DATA prime2vec<>+8(SB)/8, $PRIME2
DATA prime2vec<>+16(SB)/8, $PRIME2
DATA prime2vec<>+24(SB)/8, $PRIME2
GLOBL prime2vec<>(SB), RODATA|NOPTR, $32

DATA prime3vec<>+0(SB)/8, $PRIME3
DATA prime3vec<>+8(SB)/8, $PRIME3
DATA prime3vec<>+16(SB)/8, $PRIME3
DATA prime3vec<>+24(SB)/8, $PRIME3
GLOBL prime3vec<>(SB), RODATA|NOPTR, $32

DATA prime4vec<>+0(SB)/8, $PRIME4
DATA prime4vec<>+8(SB)/8, $PRIME4
DATA prime4vec<>+16(SB)/8, $PRIME4
DATA prime4vec<>+24(SB)/8, $PRIME4
GLOBL prime4vec<>(SB), RODATA|NOPTR, $32

DATA prime5vec<>+0(SB)/8, $PRIME5
DATA prime5vec<>+8(SB)/8, $PRIME5
DATA prime5vec<>+16(SB)/8, $PRIME5
DATA prime5vec<>+24(SB)/8, $PRIME5
GLOBL prime5vec<>(SB), RODATA|NOPTR, $32

DATA prime5plus1vec<>+0(SB)/8, $PRIME5+1
DATA prime5plus1vec<>+8(SB)/8, $PRIME5+1
DATA prime5plus1vec<>+16(SB)/8, $PRIME5+1
DATA prime5plus1vec<>+24(SB)/8, $PRIME5+1
GLOBL prime5plus1vec<>(SB), RODATA|NOPTR, $32

DATA prime5plus2vec<>+0(SB)/8, $PRIME5+2
DATA prime5plus2vec<>+8(SB)/8, $PRIME5+2
DATA prime5plus2vec<>+16(SB)/8, $PRIME5+2
DATA prime5plus2vec<>+24(SB)/8, $PRIME5+2
GLOBL prime5plus2vec<>(SB), RODATA|NOPTR, $32

DATA prime5plus4vec<>+0(SB)/8, $PRIME5+4
DATA prime5plus4vec<>+8(SB)/8, $PRIME5+4
DATA prime5plus4vec<>+16(SB)/8, $PRIME5+4
DATA prime5plus4vec<>+24(SB)/8, $PRIME5+4
GLOBL prime5plus4vec<>(SB), RODATA|NOPTR, $32

DATA prime5plus8vec<>+0(SB)/8, $PRIME5+8
DATA prime5plus8vec<>+8(SB)/8, $PRIME5+8
DATA prime5plus8vec<>+16(SB)/8, $PRIME5+8
DATA prime5plus8vec<>+24(SB)/8, $PRIME5+8
GLOBL prime5plus8vec<>(SB), RODATA|NOPTR, $32

DATA lowbytemask<>+0(SB)/8, $0xFF
DATA lowbytemask<>+8(SB)/8, $0xFF
DATA lowbytemask<>+16(SB)/8, $0xFF
DATA lowbytemask<>+24(SB)/8, $0xFF
GLOBL lowbytemask<>(SB), RODATA|NOPTR, $32

#define mulvec4x64(tmp1, tmp2, a, b, m) \
    VPSRLQ $32, b, m \
    VPSRLQ $32, a, tmp2 \
    VPMULUDQ a, m, m \
    VPMULUDQ b, tmp2, tmp2 \
    VPMULUDQ a, b, tmp1 \
    VPADDQ tmp2, m, m \
    VPSLLQ $32, m, m \
    VPADDQ m, tmp1, m

#define rotvec4x64(tmp, rot, acc) \
    VMOVDQA acc, tmp \
    VPSRLQ $(64 - rot), tmp, tmp \
    VPSLLQ $rot, acc, acc \
    VPOR tmp, acc, acc

#define round4x64(tmp1, tmp2, input, acc) \
    mulvec4x64(tmp1, tmp2, prime2YMM, input, acc) \
    VPXOR input, input, input \
    VPADDQ input, acc, input \
    rotvec4x64(tmp1, 31, input) \
    mulvec4x64(tmp1, tmp2, prime1YMM, input, acc)

#define avalanche4x64(tmp1, tmp2, tmp3, acc) \
    VMOVDQA acc, tmp1 \
    VPSRLQ $33, tmp1, tmp1 \
    VPXOR acc, tmp1, tmp1 \
    mulvec4x64(tmp2, tmp3, prime2YMM, tmp1, acc) \
    VMOVDQA acc, tmp1 \
    VPSRLQ $29, tmp1, tmp1 \
    VPXOR acc, tmp1, tmp1 \
    mulvec4x64(tmp2, tmp3, prime3YMM, tmp1, acc) \
    VMOVDQA acc, tmp1 \
    VPSRLQ $32, tmp1, tmp1 \
    VPXOR tmp1, acc, acc

#define round(input, acc) \
	IMULQ prime2, input \
	ADDQ  input, acc \
	ROLQ  $31, acc \
	IMULQ prime1, acc

#define avalanche(tmp, acc) \
    MOVQ acc, tmp \
    SHRQ $33, tmp \
    XORQ tmp, acc \
    IMULQ prime2, acc \
    MOVQ acc, tmp \
    SHRQ $29, tmp \
    XORQ tmp, acc \
    IMULQ prime3, acc \
    MOVQ acc, tmp \
    SHRQ $32, tmp \
    XORQ tmp, acc

// func MultiSum64Uint8(h []uint64, v []uint8) int
TEXT ·MultiSum64Uint8(SB), NOSPLIT, $0-54
    MOVQ $PRIME1, prime1
    MOVQ $PRIME2, prime2
    MOVQ $PRIME3, prime3
    MOVQ $PRIME5, prime5

    MOVQ h_base+0(FP), AX
    MOVQ h_len+8(FP), CX
    MOVQ v_base+24(FP), BX
    MOVQ v_len+32(FP), DX

    CMPQ CX, DX
    CMOVQGT DX, CX
    MOVQ CX, ret+48(FP)

    XORQ SI, SI
    MOVQ CX, DI
    SHRQ $3, DI
    SHLQ $3, DI

    CMPQ DI, $8
    JB loop

    VMOVDQA prime1vec<>(SB), prime1YMM
    VMOVDQA prime2vec<>(SB), prime2YMM
    VMOVDQA prime3vec<>(SB), prime3YMM
    VMOVDQA prime5vec<>(SB), prime5YMM
loop4x64:
    VMOVDQA prime5plus1vec<>(SB), Y0
    VMOVDQA prime5plus1vec<>(SB), Y3

    VPMOVZXBQ (BX)(SI*1), Y1
    VPMOVZXBQ 4(BX)(SI*1), Y4

    mulvec4x64(tmp1YMM, tmp2YMM, prime5YMM, Y1, Y2)
    mulvec4x64(tmp4YMM, tmp5YMM, prime5YMM, Y4, Y5)

    VPXOR Y2, Y0, Y0
    VPXOR Y5, Y3, Y3

    rotvec4x64(tmp1YMM, 11, Y0)
    rotvec4x64(tmp1YMM, 11, Y3)

    mulvec4x64(tmp1YMM, tmp2YMM, prime1YMM, Y0, Y1)
    mulvec4x64(tmp4YMM, tmp5YMM, prime1YMM, Y3, Y4)

    avalanche4x64(tmp1YMM, tmp2YMM, tmp3YMM, Y1)
    avalanche4x64(tmp4YMM, tmp5YMM, tmp6YMM, Y4)

    VMOVDQU Y1, (AX)(SI*8)
    VMOVDQU Y4, 32(AX)(SI*8)

    ADDQ $8, SI
    CMPQ SI, DI
    JB loop4x64
    VZEROUPPER
loop:
    CMPQ SI, CX
    JE done

    MOVQ $PRIME5+1, R8
    MOVBQZX (BX)(SI*1), R9

    IMULQ prime5, R9
    XORQ R9, R8
    ROLQ $11, R8
    IMULQ prime1, R8
    avalanche(R9, R8)

    MOVQ R8, (AX)(SI*8)

    INCQ SI
    JMP loop
done:
    RET

// func MultiSum64Uint16(h []uint64, v []uint16) int
TEXT ·MultiSum64Uint16(SB), NOSPLIT, $0-54
    MOVQ $PRIME1, prime1
    MOVQ $PRIME2, prime2
    MOVQ $PRIME3, prime3
    MOVQ $PRIME5, prime5

    MOVQ h_base+0(FP), AX
    MOVQ h_len+8(FP), CX
    MOVQ v_base+24(FP), BX
    MOVQ v_len+32(FP), DX

    CMPQ CX, DX
    CMOVQGT DX, CX
    MOVQ CX, ret+48(FP)

    XORQ SI, SI
    MOVQ CX, DI
    SHRQ $3, DI
    SHLQ $3, DI

    CMPQ DI, $8
    JB loop

    VMOVDQA prime1vec<>(SB), prime1YMM
    VMOVDQA prime2vec<>(SB), prime2YMM
    VMOVDQA prime3vec<>(SB), prime3YMM
    VMOVDQA prime5vec<>(SB), prime5YMM
loop4x64:
    VMOVDQA prime5plus2vec<>(SB), Y0
    VMOVDQA prime5plus2vec<>(SB), Y3

    VPMOVZXWQ (BX)(SI*2), Y1
    VPMOVZXWQ 8(BX)(SI*2), Y4
    VPSRLQ $8, Y1, tmp3YMM
    VPSRLQ $8, Y4, tmp6YMM
    VPAND lowbytemask<>(SB), Y1, Y1
    VPAND lowbytemask<>(SB), Y4, Y4

    mulvec4x64(tmp1YMM, tmp2YMM, prime5YMM, Y1, Y2)
    mulvec4x64(tmp4YMM, tmp5YMM, prime5YMM, Y4, Y5)
    VPXOR Y2, Y0, Y0
    VPXOR Y5, Y3, Y3
    rotvec4x64(tmp1YMM, 11, Y0)
    rotvec4x64(tmp1YMM, 11, Y3)
    mulvec4x64(tmp1YMM, tmp2YMM, prime1YMM, Y0, Y1)
    mulvec4x64(tmp4YMM, tmp5YMM, prime1YMM, Y3, Y4)

    mulvec4x64(tmp1YMM, tmp2YMM, prime5YMM, tmp3YMM, Y2)
    mulvec4x64(tmp4YMM, tmp5YMM, prime5YMM, tmp6YMM, Y5)
    VPXOR Y2, Y1, Y0
    VPXOR Y5, Y4, Y3
    rotvec4x64(tmp1YMM, 11, Y0)
    rotvec4x64(tmp1YMM, 11, Y3)
    mulvec4x64(tmp1YMM, tmp2YMM, prime1YMM, Y0, Y1)
    mulvec4x64(tmp4YMM, tmp5YMM, prime1YMM, Y3, Y4)

    avalanche4x64(tmp1YMM, tmp2YMM, tmp3YMM, Y1)
    avalanche4x64(tmp4YMM, tmp5YMM, tmp6YMM, Y4)

    VMOVDQU Y1, (AX)(SI*8)
    VMOVDQU Y4, 32(AX)(SI*8)

    ADDQ $8, SI
    CMPQ SI, DI
    JB loop4x64
    VZEROUPPER
loop:
    CMPQ SI, CX
    JE done

    MOVQ $PRIME5+2, R8
    MOVWQZX (BX)(SI*2), R9

    MOVQ R9, R10
    SHRQ $8, R10
    ANDQ $0xFF, R9

    IMULQ prime5, R9
    XORQ R9, R8
    ROLQ $11, R8
    IMULQ prime1, R8

    IMULQ prime5, R10
    XORQ R10, R8
    ROLQ $11, R8
    IMULQ prime1, R8

    avalanche(R9, R8)

    MOVQ R8, (AX)(SI*8)

    INCQ SI
    JMP loop
done:
    RET

// func MultiSum64Uint32(h []uint64, v []uint32) int
TEXT ·MultiSum64Uint32(SB), NOSPLIT, $0-54
    MOVQ $PRIME1, prime1
    MOVQ $PRIME2, prime2
    MOVQ $PRIME3, prime3

    MOVQ h_base+0(FP), AX
    MOVQ h_len+8(FP), CX
    MOVQ v_base+24(FP), BX
    MOVQ v_len+32(FP), DX

    CMPQ CX, DX
    CMOVQGT DX, CX
    MOVQ CX, ret+48(FP)

    XORQ SI, SI
    MOVQ CX, DI
    SHRQ $3, DI
    SHLQ $3, DI

    CMPQ DI, $8
    JB loop

    VMOVDQA prime1vec<>(SB), prime1YMM
    VMOVDQA prime2vec<>(SB), prime2YMM
    VMOVDQA prime3vec<>(SB), prime3YMM
loop4x64:
    VMOVDQA prime5plus4vec<>(SB), Y0
    VMOVDQA prime5plus4vec<>(SB), Y3

    VPMOVZXDQ (BX)(SI*4), Y1
    VPMOVZXDQ 16(BX)(SI*4), Y4

    mulvec4x64(tmp1YMM, tmp2YMM, prime1YMM, Y1, Y2)
    mulvec4x64(tmp4YMM, tmp5YMM, prime1YMM, Y4, Y5)

    VPXOR Y2, Y0, Y0
    VPXOR Y5, Y3, Y3

    rotvec4x64(tmp1YMM, 23, Y0)
    rotvec4x64(tmp1YMM, 23, Y3)

    mulvec4x64(tmp1YMM, tmp2YMM, prime2YMM, Y0, Y1)
    mulvec4x64(tmp4YMM, tmp5YMM, prime2YMM, Y3, Y4)

    VPADDQ prime3YMM, Y1, Y1
    VPADDQ prime3YMM, Y4, Y4

    avalanche4x64(tmp1YMM, tmp2YMM, tmp3YMM, Y1)
    avalanche4x64(tmp4YMM, tmp5YMM, tmp6YMM, Y4)

    VMOVDQU Y1, (AX)(SI*8)
    VMOVDQU Y4, 32(AX)(SI*8)

    ADDQ $8, SI
    CMPQ SI, DI
    JB loop4x64
    VZEROUPPER
loop:
    CMPQ SI, CX
    JE done

    MOVQ $PRIME5+4, R8
    MOVLQZX (BX)(SI*4), R9

    IMULQ prime1, R9
    XORQ R9, R8
    ROLQ $23, R8
    IMULQ prime2, R8
    ADDQ prime3, R8
    avalanche(R9, R8)

    MOVQ R8, (AX)(SI*8)

    INCQ SI
    JMP loop
done:
    RET

// func MultiSum64Uint64(h []uint64, v []uint64) int
TEXT ·MultiSum64Uint64(SB), NOSPLIT, $0-54
    MOVQ $PRIME1, prime1
    MOVQ $PRIME2, prime2
    MOVQ $PRIME3, prime3
    MOVQ $PRIME4, prime4

    MOVQ h_base+0(FP), AX
    MOVQ h_len+8(FP), CX
    MOVQ v_base+24(FP), BX
    MOVQ v_len+32(FP), DX

    CMPQ CX, DX
    CMOVQGT DX, CX
    MOVQ CX, ret+48(FP)

    XORQ SI, SI
    MOVQ CX, DI
    SHRQ $3, DI
    SHLQ $3, DI

    CMPQ DI, $8
    JB loop

    VMOVDQA prime1vec<>(SB), prime1YMM
    VMOVDQA prime2vec<>(SB), prime2YMM
    VMOVDQA prime3vec<>(SB), prime3YMM
    VMOVDQA prime4vec<>(SB), prime4YMM
loop4x64:
    VMOVDQA prime5plus8vec<>(SB), Y0
    VMOVDQA prime5plus8vec<>(SB), Y3

    VMOVDQU (BX)(SI*8), Y1
    VMOVDQU 32(BX)(SI*8), Y4

    VPXOR Y2, Y2, Y2
    VPXOR Y5, Y5, Y5

    round4x64(tmp1YMM, tmp2YMM, Y1, Y2)
    round4x64(tmp4YMM, tmp5YMM, Y4, Y5)

    VPXOR Y2, Y0, Y0
    VPXOR Y5, Y3, Y3

    rotvec4x64(tmp1YMM, 27, Y0)
    rotvec4x64(tmp3YMM, 27, Y3)

    mulvec4x64(tmp1YMM, tmp2YMM, prime1YMM, Y0, Y1)
    mulvec4x64(tmp4YMM, tmp5YMM, prime1YMM, Y3, Y4)

    VPADDQ prime4YMM, Y1, Y1
    VPADDQ prime4YMM, Y4, Y4

    avalanche4x64(tmp1YMM, tmp2YMM, tmp3YMM, Y1)
    avalanche4x64(tmp4YMM, tmp5YMM, tmp6YMM, Y4)

    VMOVDQU Y1, (AX)(SI*8)
    VMOVDQU Y4, 32(AX)(SI*8)

    ADDQ $8, SI
    CMPQ SI, DI
    JB loop4x64
    VZEROUPPER
loop:
    CMPQ SI, CX
    JE done

    MOVQ $PRIME5+8, R8
    MOVQ (BX)(SI*8), R9

    XORQ R10, R10
    round(R9, R10)
    XORQ R10, R8
    ROLQ $27, R8
    IMULQ prime1, R8
    ADDQ prime4, R8
    avalanche(R9, R8)

    MOVQ R8, (AX)(SI*8)

    INCQ SI
    JMP loop
done:
    RET

// func MultiSum64Uint128(h []uint64, v [][16]byte) int
TEXT ·MultiSum64Uint128(SB), NOSPLIT, $0-54
    MOVQ $PRIME1, prime1
    MOVQ $PRIME2, prime2
    MOVQ $PRIME3, prime3
    MOVQ $PRIME4, prime4

    MOVQ h_base+0(FP), AX
    MOVQ h_len+8(FP), CX
    MOVQ v_base+24(FP), BX
    MOVQ v_len+32(FP), DX

    CMPQ CX, DX
    CMOVQGT DX, CX
    MOVQ CX, ret+48(FP)

    // I attempted to vectorize this algorithm but it yielded similar or
    // worse performance:
    //
    // * When computing one hash per 128 bit halves of YMM registers,
    //   throughput was half of what we get with the scalar version.
    //
    // * When computing two hashes per 128 bit halves of YMM register,
    //   the performance were similar to the scalar version with an
    //   excessive amount of added complexity.
    //
    // Hashing 128 bits inputs performs two iterations of the 64 bits loop,
    // so it makes sense that the vectorized version ended up being half as
    // fast as hashing 64 bits values, it is running 2x the number of CPU
    // instructions, and does not offer more opportunities to parallelize
    // computation..
    //
    // I also tried unrolling two iterations of the loop using scalar
    // instructions but the throughput improvements were minimal for a lot
    // of added complexity.
    XORQ SI, SI
loop:
    CMPQ SI, CX
    JE done

    MOVQ $PRIME5+16, R8
    MOVQ (BX), DX
    MOVQ 8(BX), DI

    XORQ R9, R9
    XORQ R10, R10
    round(DX, R9)
    round(DI, R10)

    XORQ R9, R8
    ROLQ $27, R8
    IMULQ prime1, R8
    ADDQ prime4, R8

    XORQ R10, R8
    ROLQ $27, R8
    IMULQ prime1, R8
    ADDQ prime4, R8

    avalanche(R9, R8)

    MOVQ R8, (AX)(SI*8)
    ADDQ $16, BX
    INCQ SI
    JMP loop
done:
    RET