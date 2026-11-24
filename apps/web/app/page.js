import Link from 'next/link';

export default function Home() {
  return (
    <main>
      <h1>AegisMesh</h1>
      <p className="muted">Reports made in the field work without a connection and are sent when it returns.</p>
      <div className="home-links">
        <Link href="/field">Field app: report an incident</Link>
        <Link href="/command">Command center: live map</Link>
      </div>
    </main>
  );
}
