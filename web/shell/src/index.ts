// Module Federation async boundary: the real app lives in bootstrap.tsx. This
// indirection lets the MF runtime negotiate shared singletons before any shared
// module is evaluated.
import "./bootstrap";
