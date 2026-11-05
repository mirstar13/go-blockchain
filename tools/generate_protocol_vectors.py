"""Independent protocol-v1 vectors: Python struct/hashlib + cryptography Ed25519.
Run manually from the repository root; never invoked by Go tests.
Requires the Python cryptography package. Do not regenerate to mask a regression.
Seeds/public keys below are RFC 8032 test vectors, not real wallet secrets.
"""
import hashlib
import json
import struct
from pathlib import Path
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat


def u64(n):
    return struct.pack('>Q', n)


def string(s):
    b = s.encode('utf-8')
    return u64(len(b)) + b


network = 'go-blockchain-v1'
seeds = [
    '9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60',
    '4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb',
]
public = [
    'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a',
    '3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c',
]
vectors = []
for i, (amount, nonce, sequence) in enumerate([(10, 1, 7), (2**64-1, 2**64-1, 2**64-1)]):
    key = Ed25519PrivateKey.from_private_bytes(bytes.fromhex(seeds[i]))
    sender = key.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)
    assert sender.hex() == public[i]
    recipient = bytes.fromhex(public[1-i])
    message = string('ledger/transaction/sign/v1') + string(network) + sender + recipient + u64(amount) + u64(nonce)
    signature = key.sign(message)
    txid = hashlib.sha256(string('ledger/transaction/id/v1') + message + signature).digest()
    cancellation = string('ledger/cancel/v1') + string(network) + txid + sender + u64(sequence)
    previous = bytes(range(32))
    block_message = (string('ledger/block/v1') + u64(1) + u64(50) + u64(100) + string(network)
                     + u64(7) + u64(1700000000) + previous + u64(1) + txid + recipient
                     + u64(50) + u64(16) + u64(123456789))
    vectors.append(dict(seed=seeds[i], sender=sender.hex(), recipient=recipient.hex(), amount=amount,
                        nonce=nonce, sequence=sequence, signing_bytes=message.hex(), signature=signature.hex(),
                        transaction_id=txid.hex(), cancellation_bytes=cancellation.hex(),
                        cancellation_signature=key.sign(cancellation).hex(), previous=previous.hex(),
                        block_hash=hashlib.sha256(block_message).hexdigest()))
Path('ledger/testdata/protocol_v1.json').write_text(json.dumps(vectors, indent=2)+'\n', encoding='utf-8')
