export function Hero() {
  return (
    <div className="bg-gradient-to-r from-purple-500 to-indigo-600 rounded-2xl">
      <h1 className="bg-clip-text text-transparent">Revolutionize your team</h1>
      <div className="border-l-4 border-violet-500 rounded-2xl animate-bounce">✨ Magic</div>
      <img src={logo} />
      <button className="outline-none rounded-2xl">Go</button>
    </div>
  );
}
